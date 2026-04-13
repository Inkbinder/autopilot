package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/Inkbinder/autopilot/internal/runstate"
)

const dockerDaemonHost = "unix:///var/run/docker.sock"
const dockerWorkspaceMountPath = "/workspace"

type dockerAPIClient interface {
	ImagePull(ctx context.Context, refStr string, options image.PullOptions) (io.ReadCloser, error)
	ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error)
	ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error
	ContainerExecCreate(ctx context.Context, containerID string, options container.ExecOptions) (container.ExecCreateResponse, error)
	ContainerExecAttach(ctx context.Context, execID string, config container.ExecAttachOptions) (dockertypes.HijackedResponse, error)
	ContainerExecInspect(ctx context.Context, execID string) (container.ExecInspect, error)
	ContainerStop(ctx context.Context, containerID string, options container.StopOptions) error
	ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error
	Close() error
}

type dockerWorkspace struct {
	containerID   string
	workspacePath string
	containerPath string
}

type DockerProvider struct {
	root        string
	image       string
	auditWriter runstate.Writer
	logger      *slog.Logger
	client      dockerAPIClient

	mu         sync.RWMutex
	workspaces map[string]dockerWorkspace
}

var _ WorkspaceProvider = (*DockerProvider)(nil)
var _ contextAwareWorkspaceProvider = (*DockerProvider)(nil)
var _ workspacePathProvider = (*DockerProvider)(nil)

func NewDockerProvider(config WorkspaceConfig) (*DockerProvider, error) {
	return NewDockerProviderWithOptions(config, ProviderOptions{})
}

func NewDockerProviderWithOptions(config WorkspaceConfig, options ProviderOptions) (*DockerProvider, error) {
	root := strings.TrimSpace(config.Root)
	if root == "" {
		return nil, fmt.Errorf("workspace.root is required")
	}
	imageName := strings.TrimSpace(config.Image)
	if imageName == "" {
		return nil, fmt.Errorf("workspace.image is required for docker provider")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	factory := options.dockerClientFactory
	if factory == nil {
		factory = defaultDockerClientFactory
	}
	dockerClient, err := factory()
	if err != nil {
		return nil, err
	}
	if dockerClient == nil {
		return nil, fmt.Errorf("docker client factory returned nil client")
	}
	return &DockerProvider{
		root:        absRoot,
		image:       imageName,
		auditWriter: options.AuditWriter,
		logger:      options.Logger,
		client:      dockerClient,
		workspaces:  map[string]dockerWorkspace{},
	}, nil
}

func defaultDockerClientFactory() (dockerAPIClient, error) {
	return client.NewClientWithOpts(client.WithHost(dockerDaemonHost), client.WithAPIVersionNegotiation())
}

func (provider *DockerProvider) Setup(issueID string, config WorkspaceConfig) (string, error) {
	if err := os.MkdirAll(provider.root, 0o755); err != nil {
		return "", err
	}
	workspacePath := provider.WorkspacePath(issueID)
	if err := validateWorkspacePath(provider.root, workspacePath); err != nil {
		return "", err
	}
	stat, err := os.Stat(workspacePath)
	if err == nil {
		if !stat.IsDir() {
			return "", fmt.Errorf("workspace path exists and is not a directory: %s", workspacePath)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	} else if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		return "", err
	}

	if existing, ok := provider.trackedWorkspace(issueID); ok {
		return existing.workspacePath, nil
	}

	imageName := strings.TrimSpace(config.Image)
	if imageName == "" {
		imageName = provider.image
	}
	ctx := context.Background()
	if err := provider.pullImage(ctx, imageName); err != nil {
		return "", err
	}
	containerName := provider.containerName(issueID)
	if err := provider.destroyContainer(ctx, containerName); err != nil {
		return "", err
	}
	created, err := provider.client.ContainerCreate(ctx, &container.Config{
		Image:      imageName,
		Cmd:        []string{"tail", "-f", "/dev/null"},
		WorkingDir: dockerWorkspaceMountPath,
		Labels: map[string]string{
			"autopilot.issue_identifier":         issueID,
			"autopilot.workspace_path":           workspacePath,
			"autopilot.container_workspace_path": dockerWorkspaceMountPath,
			"autopilot.workspace_type":           "docker",
		},
	}, &container.HostConfig{
		Mounts: []mount.Mount{{
			Type:   mount.TypeBind,
			Source: workspacePath,
			Target: dockerWorkspaceMountPath,
		}},
	}, nil, nil, containerName)
	if err != nil {
		return "", err
	}
	if err := provider.client.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		_ = provider.destroyContainer(ctx, created.ID)
		return "", err
	}
	provider.trackWorkspace(issueID, workspacePath, dockerWorkspaceMountPath, created.ID)
	return workspacePath, nil
}

func (provider *DockerProvider) Execute(command string, args []string, dir string) (string, error) {
	return provider.ExecuteContext(context.Background(), command, args, dir)
}

func (provider *DockerProvider) ExecuteContext(ctx context.Context, command string, args []string, dir string) (output string, err error) {
	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("workspace command is required")
	}
	defer provider.recordAuditEvent(ctx, command, args, dir, &output, &err)

	workspaceDir := strings.TrimSpace(dir)
	if workspaceDir == "" {
		return "", fmt.Errorf("workspace dir is required for docker provider")
	}
	absDir, absErr := filepath.Abs(workspaceDir)
	if absErr != nil {
		return "", absErr
	}
	workspace, executionDir, resolveErr := provider.workspaceForDir(absDir)
	if resolveErr != nil {
		return "", resolveErr
	}
	execConfig := container.ExecOptions{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          append([]string{command}, args...),
		WorkingDir:   executionDir,
	}
	created, createErr := provider.client.ContainerExecCreate(ctx, workspace.containerID, execConfig)
	if createErr != nil {
		return "", createErr
	}
	attached, attachErr := provider.client.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{Tty: false})
	if attachErr != nil {
		return "", attachErr
	}
	defer attached.Close()

	output, err = readDockerExecOutput(attached)
	if err != nil {
		return output, err
	}
	inspected, inspectErr := provider.client.ContainerExecInspect(ctx, created.ID)
	if inspectErr != nil {
		return output, inspectErr
	}
	if inspected.ExitCode != 0 {
		return output, fmt.Errorf("docker exec exit code %d", inspected.ExitCode)
	}
	return output, nil
}

func (provider *DockerProvider) ExecuteStream(ctx context.Context, command string, args []string, dir string) (ExecutionStream, error) {
	if strings.TrimSpace(command) == "" {
		return nil, fmt.Errorf("workspace command is required")
	}
	workspaceDir := strings.TrimSpace(dir)
	if workspaceDir == "" {
		return nil, fmt.Errorf("workspace dir is required for docker provider")
	}
	absDir, err := filepath.Abs(workspaceDir)
	if err != nil {
		return nil, err
	}
	workspace, executionDir, err := provider.workspaceForDir(absDir)
	if err != nil {
		return nil, err
	}
	created, err := provider.client.ContainerExecCreate(ctx, workspace.containerID, container.ExecOptions{
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          append([]string{command}, args...),
		WorkingDir:   executionDir,
	})
	if err != nil {
		return nil, err
	}
	attached, err := provider.client.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{Tty: false})
	if err != nil {
		return nil, err
	}
	stdoutReader, stdoutWriter := io.Pipe()
	stderrReader, stderrWriter := io.Pipe()
	waitCh := make(chan error, 1)
	closedCh := make(chan struct{})
	go func() {
		copyErr := copyDockerExecStreams(attached, stdoutWriter, stderrWriter)
		if copyErr != nil {
			_ = stdoutWriter.CloseWithError(copyErr)
			_ = stderrWriter.CloseWithError(copyErr)
			select {
			case <-closedCh:
				waitCh <- nil
			default:
				waitCh <- copyErr
			}
			return
		}
		_ = stdoutWriter.Close()
		_ = stderrWriter.Close()
		select {
		case <-closedCh:
			waitCh <- nil
			return
		default:
		}
		inspected, inspectErr := provider.client.ContainerExecInspect(context.Background(), created.ID)
		if inspectErr != nil {
			waitCh <- inspectErr
			return
		}
		if inspected.ExitCode != 0 {
			waitCh <- fmt.Errorf("docker exec exit code %d", inspected.ExitCode)
			return
		}
		waitCh <- nil
	}()
	closeStream := func() error {
		select {
		case <-closedCh:
		default:
			close(closedCh)
		}
		_ = attached.CloseWrite()
		attached.Close()
		return nil
	}
	return newExecutionStream(attached.Conn, stdoutReader, stderrReader, executionDir, nil, func() error {
		return <-waitCh
	}, closeStream, nil), nil
}

func (provider *DockerProvider) Teardown(issueID string) error {
	workspacePath := provider.WorkspacePath(issueID)
	if err := validateWorkspacePath(provider.root, workspacePath); err != nil {
		return err
	}
	containerRef := provider.containerRef(issueID)
	if err := provider.destroyContainer(context.Background(), containerRef); err != nil {
		return err
	}
	provider.untrackWorkspace(issueID)

	stat, err := os.Stat(workspacePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !stat.IsDir() {
		return fmt.Errorf("workspace path exists and is not a directory: %s", workspacePath)
	}
	return os.RemoveAll(workspacePath)
}

func (provider *DockerProvider) WorkspacePath(issueID string) string {
	return filepath.Join(provider.root, SanitizeWorkspaceKey(issueID))
}

func (provider *DockerProvider) pullImage(ctx context.Context, imageName string) error {
	stream, err := provider.client.ImagePull(ctx, imageName, image.PullOptions{})
	if err != nil {
		return err
	}
	defer stream.Close()
	_, err = io.Copy(io.Discard, stream)
	return err
}

func (provider *DockerProvider) destroyContainer(ctx context.Context, containerRef string) error {
	if strings.TrimSpace(containerRef) == "" {
		return nil
	}
	stopTimeout := 5
	_ = provider.client.ContainerStop(ctx, containerRef, container.StopOptions{Timeout: &stopTimeout})
	err := provider.client.ContainerRemove(ctx, containerRef, container.RemoveOptions{Force: true})
	if err != nil && !client.IsErrNotFound(err) {
		return err
	}
	return nil
}

func (provider *DockerProvider) containerName(issueID string) string {
	rootDigest := sha256.Sum256([]byte(provider.root))
	issueDigest := sha256.Sum256([]byte(issueID))
	key := SanitizeWorkspaceKey(issueID)
	if len(key) > 48 {
		key = key[:48]
	}
	return fmt.Sprintf("autopilot-%x-%s-%x", rootDigest[:4], key, issueDigest[:4])
}

func (provider *DockerProvider) trackWorkspace(issueID string, workspacePath string, containerPath string, containerID string) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.workspaces[SanitizeWorkspaceKey(issueID)] = dockerWorkspace{containerID: containerID, workspacePath: workspacePath, containerPath: containerPath}
}

func (provider *DockerProvider) trackedWorkspace(issueID string) (dockerWorkspace, bool) {
	provider.mu.RLock()
	defer provider.mu.RUnlock()
	workspace, ok := provider.workspaces[SanitizeWorkspaceKey(issueID)]
	return workspace, ok
}

func (provider *DockerProvider) untrackWorkspace(issueID string) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	delete(provider.workspaces, SanitizeWorkspaceKey(issueID))
}

func (provider *DockerProvider) containerRef(issueID string) string {
	if workspace, ok := provider.trackedWorkspace(issueID); ok {
		return workspace.containerID
	}
	return provider.containerName(issueID)
}

func (provider *DockerProvider) workspaceForDir(dir string) (dockerWorkspace, string, error) {
	provider.mu.RLock()
	defer provider.mu.RUnlock()
	longestMatch := -1
	resolvedWorkspace := dockerWorkspace{}
	resolvedDir := ""
	for _, workspace := range provider.workspaces {
		if dir == workspace.workspacePath || strings.HasPrefix(dir, workspace.workspacePath+string(os.PathSeparator)) {
			if len(workspace.workspacePath) > longestMatch {
				longestMatch = len(workspace.workspacePath)
				relative := strings.TrimPrefix(strings.TrimPrefix(dir, workspace.workspacePath), string(os.PathSeparator))
				resolvedWorkspace = workspace
				if strings.TrimSpace(relative) == "" {
					resolvedDir = workspace.containerPath
				} else {
					resolvedDir = filepath.Join(workspace.containerPath, relative)
				}
			}
		}
	}
	if resolvedWorkspace.containerID == "" {
		return dockerWorkspace{}, "", fmt.Errorf("docker workspace container not found for dir: %s", dir)
	}
	return resolvedWorkspace, resolvedDir, nil
}

func readDockerExecOutput(response dockertypes.HijackedResponse) (string, error) {
	var output bytes.Buffer
	mediaType, ok := response.MediaType()
	if ok && mediaType == dockertypes.MediaTypeMultiplexedStream {
		_, err := stdcopy.StdCopy(&output, &output, response.Reader)
		return output.String(), err
	}
	_, err := io.Copy(&output, response.Reader)
	return output.String(), err
}

func copyDockerExecStreams(response dockertypes.HijackedResponse, stdout io.Writer, stderr io.Writer) error {
	mediaType, ok := response.MediaType()
	if ok && mediaType == dockertypes.MediaTypeMultiplexedStream {
		_, err := stdcopy.StdCopy(stdout, stderr, response.Reader)
		return err
	}
	_, err := io.Copy(stdout, response.Reader)
	return err
}

func (provider *DockerProvider) recordAuditEvent(ctx context.Context, command string, args []string, dir string, output *string, execErr *error) {
	auditErr := runstate.RecordAuditEvent(ctx, provider.auditWriter, "workspace_exec", map[string]any{
		"command": command,
		"args":    args,
		"dir":     dir,
		"output":  *output,
		"success": *execErr == nil,
		"error":   errorString(*execErr),
	})
	if auditErr != nil {
		provider.logAuditFailure(ctx, auditErr)
	}
}

func (provider *DockerProvider) logAuditFailure(ctx context.Context, err error) {
	if provider.logger == nil {
		return
	}
	metadata, _ := runstate.MetadataFromContext(ctx)
	provider.logger.With(slog.String("repo", strings.TrimSpace(metadata.Repo)), slog.String("issue_id", strings.TrimSpace(metadata.IssueID))).Warn("workspace audit write failed", slog.Any("error", err))
}
