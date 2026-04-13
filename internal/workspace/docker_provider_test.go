package workspace

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/pkg/stdcopy"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/Inkbinder/autopilot/internal/runstate"
	"github.com/Inkbinder/autopilot/internal/workflow"
)

type fakeDockerClient struct {
	imagePulls         []string
	containerCreates   []fakeContainerCreateCall
	containerStarts    []string
	execCreates        []fakeExecCreateCall
	execAttaches       []string
	execInspects       []string
	containerStops     []string
	containerRemovals  []string
	imagePullErr       error
	containerCreateErr error
	containerStartErr  error
	execCreateErr      error
	execAttachErr      error
	execInspectErr     error
	containerStopErr   error
	containerRemoveErr error
	createdContainerID string
	execID             string
	execStdout         string
	execStderr         string
	execMediaType      string
	execExitCode       int
	closed             bool
}

type fakeContainerCreateCall struct {
	Config     container.Config
	HostConfig container.HostConfig
	Name       string
}

type fakeExecCreateCall struct {
	ContainerID string
	Options     container.ExecOptions
}

func (client *fakeDockerClient) ImagePull(_ context.Context, refStr string, _ image.PullOptions) (io.ReadCloser, error) {
	client.imagePulls = append(client.imagePulls, refStr)
	if client.imagePullErr != nil {
		return nil, client.imagePullErr
	}
	return io.NopCloser(strings.NewReader("{}")), nil
}

func (client *fakeDockerClient) ContainerCreate(_ context.Context, config *container.Config, hostConfig *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, containerName string) (container.CreateResponse, error) {
	if client.containerCreateErr != nil {
		return container.CreateResponse{}, client.containerCreateErr
	}
	createCall := fakeContainerCreateCall{Name: containerName}
	if config != nil {
		createCall.Config = *config
	}
	if hostConfig != nil {
		createCall.HostConfig = *hostConfig
	}
	client.containerCreates = append(client.containerCreates, createCall)
	containerID := client.createdContainerID
	if containerID == "" {
		containerID = "container-1"
	}
	return container.CreateResponse{ID: containerID}, nil
}

func (client *fakeDockerClient) ContainerStart(_ context.Context, containerID string, _ container.StartOptions) error {
	client.containerStarts = append(client.containerStarts, containerID)
	return client.containerStartErr
}

func (client *fakeDockerClient) ContainerExecCreate(_ context.Context, containerID string, options container.ExecOptions) (container.ExecCreateResponse, error) {
	client.execCreates = append(client.execCreates, fakeExecCreateCall{ContainerID: containerID, Options: options})
	if client.execCreateErr != nil {
		return container.ExecCreateResponse{}, client.execCreateErr
	}
	execID := client.execID
	if execID == "" {
		execID = "exec-1"
	}
	return container.ExecCreateResponse{ID: execID}, nil
}

func (client *fakeDockerClient) ContainerExecAttach(_ context.Context, execID string, _ container.ExecAttachOptions) (dockertypes.HijackedResponse, error) {
	client.execAttaches = append(client.execAttaches, execID)
	if client.execAttachErr != nil {
		return dockertypes.HijackedResponse{}, client.execAttachErr
	}
	mediaType := client.execMediaType
	if mediaType == "" {
		mediaType = dockertypes.MediaTypeRawStream
	}
	return newFakeHijackedResponse(client.execStdout, client.execStderr, mediaType), nil
}

func (client *fakeDockerClient) ContainerExecInspect(_ context.Context, execID string) (container.ExecInspect, error) {
	client.execInspects = append(client.execInspects, execID)
	if client.execInspectErr != nil {
		return container.ExecInspect{}, client.execInspectErr
	}
	return container.ExecInspect{ExecID: execID, ExitCode: client.execExitCode}, nil
}

func (client *fakeDockerClient) ContainerStop(_ context.Context, containerID string, _ container.StopOptions) error {
	client.containerStops = append(client.containerStops, containerID)
	return client.containerStopErr
}

func (client *fakeDockerClient) ContainerRemove(_ context.Context, containerID string, _ container.RemoveOptions) error {
	client.containerRemovals = append(client.containerRemovals, containerID)
	return client.containerRemoveErr
}

func (client *fakeDockerClient) Close() error {
	client.closed = true
	return nil
}

func TestDockerProviderSetupExecuteAndTeardown(t *testing.T) {
	t.Parallel()
	fakeClient := &fakeDockerClient{execStdout: "hello", execStderr: "stderr", execMediaType: dockertypes.MediaTypeMultiplexedStream}
	root := filepath.Join(t.TempDir(), "workspaces")
	provider, err := NewDockerProviderWithOptions(workflow.WorkspaceConfig{Root: root, Image: "alpine:3.20"}, ProviderOptions{dockerClientFactory: func() (dockerAPIClient, error) {
		return fakeClient, nil
	}})
	if err != nil {
		t.Fatalf("NewDockerProviderWithOptions() error = %v", err)
	}

	workspacePath, err := provider.Setup("octo/widgets#123", workflow.WorkspaceConfig{Root: root, Image: "alpine:3.20"})
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	if filepath.Base(workspacePath) != "octo_widgets_123" {
		t.Fatalf("workspace path = %q", workspacePath)
	}
	if len(fakeClient.imagePulls) != 1 || fakeClient.imagePulls[0] != "alpine:3.20" {
		t.Fatalf("image pulls = %#v, want alpine:3.20", fakeClient.imagePulls)
	}
	if len(fakeClient.containerCreates) != 1 {
		t.Fatalf("container creates = %d, want 1", len(fakeClient.containerCreates))
	}
	created := fakeClient.containerCreates[0]
	if created.Config.Image != "alpine:3.20" {
		t.Fatalf("container image = %q, want alpine:3.20", created.Config.Image)
	}
	if created.Config.WorkingDir != dockerWorkspaceMountPath {
		t.Fatalf("container working dir = %q, want %q", created.Config.WorkingDir, dockerWorkspaceMountPath)
	}
	if len(created.Config.Cmd) != 3 || created.Config.Cmd[0] != "tail" || created.Config.Cmd[1] != "-f" || created.Config.Cmd[2] != "/dev/null" {
		t.Fatalf("container cmd = %#v, want tail -f /dev/null", created.Config.Cmd)
	}
	if created.Name != provider.containerName("octo/widgets#123") {
		t.Fatalf("container name = %q, want %q", created.Name, provider.containerName("octo/widgets#123"))
	}
	if len(created.HostConfig.Mounts) != 1 {
		t.Fatalf("mount count = %d, want 1", len(created.HostConfig.Mounts))
	}
	mounted := created.HostConfig.Mounts[0]
	if mounted.Type != mount.TypeBind || mounted.Source != workspacePath || mounted.Target != dockerWorkspaceMountPath {
		t.Fatalf("mount = %#v, want bind mount for %q", mounted, workspacePath)
	}
	subdir := filepath.Join(workspacePath, "nested")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	output, err := provider.Execute("sh", []string{"-lc", "printf hello && printf stderr >&2"}, subdir)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if output != "hellostderr" {
		t.Fatalf("Execute() output = %q, want hellostderr", output)
	}
	if len(fakeClient.execCreates) != 1 {
		t.Fatalf("exec create count = %d, want 1", len(fakeClient.execCreates))
	}
	execCall := fakeClient.execCreates[0]
	if execCall.ContainerID != "container-1" {
		t.Fatalf("exec container id = %q, want container-1", execCall.ContainerID)
	}
	if execCall.Options.WorkingDir != filepath.Join(dockerWorkspaceMountPath, "nested") {
		t.Fatalf("exec working dir = %q, want %q", execCall.Options.WorkingDir, filepath.Join(dockerWorkspaceMountPath, "nested"))
	}
	if len(execCall.Options.Cmd) != 3 || execCall.Options.Cmd[0] != "sh" {
		t.Fatalf("exec command = %#v, want sh -lc ...", execCall.Options.Cmd)
	}
	if err := provider.Teardown("octo/widgets#123"); err != nil {
		t.Fatalf("Teardown() error = %v", err)
	}
	if _, err := os.Stat(workspacePath); !os.IsNotExist(err) {
		t.Fatalf("expected workspace teardown, stat err = %v", err)
	}
	if len(fakeClient.containerRemovals) == 0 || fakeClient.containerRemovals[len(fakeClient.containerRemovals)-1] != "container-1" {
		t.Fatalf("container removals = %#v, want final removal of container-1", fakeClient.containerRemovals)
	}
	if len(fakeClient.containerStops) == 0 || fakeClient.containerStops[len(fakeClient.containerStops)-1] != "container-1" {
		t.Fatalf("container stops = %#v, want final stop of container-1", fakeClient.containerStops)
	}
}

func TestDockerProviderExecuteContextAuditsCommandOutput(t *testing.T) {
	t.Parallel()
	recorder := &auditRecorder{}
	fakeClient := &fakeDockerClient{execStdout: "audit-ok"}
	root := filepath.Join(t.TempDir(), "workspaces")
	provider, err := NewDockerProviderWithOptions(workflow.WorkspaceConfig{Root: root, Image: "alpine:3.20"}, ProviderOptions{AuditWriter: recorder, dockerClientFactory: func() (dockerAPIClient, error) {
		return fakeClient, nil
	}})
	if err != nil {
		t.Fatalf("NewDockerProviderWithOptions() error = %v", err)
	}
	workspacePath, err := provider.Setup("octo/widgets#77", workflow.WorkspaceConfig{Root: root, Image: "alpine:3.20"})
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	ctx := runstate.WithMetadata(context.Background(), runstate.Metadata{RunID: 7, IssueID: "77", Repo: "octo/widgets"})
	if _, err := provider.ExecuteContext(ctx, "sh", []string{"-lc", "printf audit-ok"}, workspacePath); err != nil {
		t.Fatalf("ExecuteContext() error = %v", err)
	}
	if len(recorder.events) != 1 {
		t.Fatalf("audit event count = %d, want 1", len(recorder.events))
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(recorder.events[0].Payload), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if payload["command"] != "sh" {
		t.Fatalf("payload.command = %#v, want sh", payload["command"])
	}
	if payload["output"] != "audit-ok" {
		t.Fatalf("payload.output = %#v, want audit-ok", payload["output"])
	}
	if payload["success"] != true {
		t.Fatalf("payload.success = %#v, want true", payload["success"])
	}
}

func TestDockerProviderExecuteStreamUsesContainerWorkingDir(t *testing.T) {
	t.Parallel()
	fakeClient := &fakeDockerClient{execStdout: "stream-ok"}
	root := filepath.Join(t.TempDir(), "workspaces")
	provider, err := NewDockerProviderWithOptions(workflow.WorkspaceConfig{Root: root, Image: "alpine:3.20"}, ProviderOptions{dockerClientFactory: func() (dockerAPIClient, error) {
		return fakeClient, nil
	}})
	if err != nil {
		t.Fatalf("NewDockerProviderWithOptions() error = %v", err)
	}
	workspacePath, err := provider.Setup("octo/widgets#55", workflow.WorkspaceConfig{Root: root, Image: "alpine:3.20"})
	if err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	subdir := filepath.Join(workspacePath, "nested")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	stream, err := provider.ExecuteStream(context.Background(), "bash", []string{"-lc", "copilot --acp --stdio"}, subdir)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	stdout, err := io.ReadAll(stream.Stdout())
	if err != nil {
		t.Fatalf("ReadAll(stdout) error = %v", err)
	}
	if string(stdout) != "stream-ok" {
		t.Fatalf("stdout = %q, want stream-ok", stdout)
	}
	stderr, err := io.ReadAll(stream.Stderr())
	if err != nil {
		t.Fatalf("ReadAll(stderr) error = %v", err)
	}
	if len(stderr) != 0 {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if stream.WorkingDir() != filepath.Join(dockerWorkspaceMountPath, "nested") {
		t.Fatalf("stream working dir = %q, want %q", stream.WorkingDir(), filepath.Join(dockerWorkspaceMountPath, "nested"))
	}
	if err := stream.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if len(fakeClient.execCreates) != 1 {
		t.Fatalf("exec create count = %d, want 1", len(fakeClient.execCreates))
	}
	execCall := fakeClient.execCreates[0]
	if !execCall.Options.AttachStdin || !execCall.Options.AttachStdout || !execCall.Options.AttachStderr {
		t.Fatalf("stream exec attachments = %#v, want stdin/stdout/stderr", execCall.Options)
	}
	if execCall.Options.WorkingDir != filepath.Join(dockerWorkspaceMountPath, "nested") {
		t.Fatalf("stream exec working dir = %q, want %q", execCall.Options.WorkingDir, filepath.Join(dockerWorkspaceMountPath, "nested"))
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func newFakeHijackedResponse(stdout string, stderr string, mediaType string) dockertypes.HijackedResponse {
	clientConn, serverConn := net.Pipe()
	go func() {
		defer serverConn.Close()
		if mediaType == dockertypes.MediaTypeMultiplexedStream {
			if stdout != "" {
				_, _ = stdcopy.NewStdWriter(serverConn, stdcopy.Stdout).Write([]byte(stdout))
			}
			if stderr != "" {
				_, _ = stdcopy.NewStdWriter(serverConn, stdcopy.Stderr).Write([]byte(stderr))
			}
			return
		}
		_, _ = io.WriteString(serverConn, stdout+stderr)
	}()
	return dockertypes.NewHijackedResponse(clientConn, mediaType)
}
