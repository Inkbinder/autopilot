package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"autopilot/internal/workflow"
)

type httpServer struct {
	server *http.Server
	port   int
}

func (server *httpServer) shutdown(ctx context.Context) error {
	if server == nil || server.server == nil {
		return nil
	}
	return server.server.Shutdown(ctx)
}

func (orchestrator *Orchestrator) effectivePort(config *workflow.Config) *int {
	if orchestrator.portOverride != nil {
		return orchestrator.portOverride
	}
	if config == nil {
		orchestrator.mu.Lock()
		defer orchestrator.mu.Unlock()
		if orchestrator.config.Server.Port == nil {
			return nil
		}
		value := *orchestrator.config.Server.Port
		return &value
	}
	if config.Server.Port == nil {
		return nil
	}
	value := *config.Server.Port
	return &value
}

func (orchestrator *Orchestrator) startHTTPServer() error {
	port := orchestrator.effectivePort(nil)
	if port == nil {
		return nil
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", orchestrator.handleDashboard)
	mux.HandleFunc("/api/v1/state", orchestrator.handleState)
	mux.HandleFunc("/api/v1/refresh", orchestrator.handleRefresh)
	mux.HandleFunc("/api/v1/", orchestrator.handleIssue)
	server := &http.Server{Handler: mux}
	orchestrator.server = &httpServer{server: server, port: listener.Addr().(*net.TCPAddr).Port}
	go func() {
		err := server.Serve(listener)
		if err != nil && err != http.ErrServerClosed {
			orchestrator.serverErrCh <- err
		}
	}()
	return nil
}

func (orchestrator *Orchestrator) handleDashboard(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/" {
		http.NotFound(writer, request)
		return
	}
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	snapshot := orchestrator.Snapshot()
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = dashboardTemplate.Execute(writer, map[string]any{
		"Snapshot": snapshot,
		"GeneratedAt": snapshot.GeneratedAt.Format(time.RFC3339),
	})
}

func (orchestrator *Orchestrator) handleState(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writeJSONError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	writeJSON(writer, http.StatusOK, orchestrator.Snapshot())
}

func (orchestrator *Orchestrator) handleRefresh(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeJSONError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	coalesced := orchestrator.TriggerRefresh()
	writeJSON(writer, http.StatusAccepted, map[string]any{
		"queued": true,
		"coalesced": coalesced,
		"requested_at": time.Now().UTC().Format(time.RFC3339),
		"operations": []string{"poll", "reconcile"},
	})
}

func (orchestrator *Orchestrator) handleIssue(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writeJSONError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	identifier, err := url.PathUnescape(strings.TrimPrefix(request.URL.Path, "/api/v1/"))
	if err != nil || strings.TrimSpace(identifier) == "" {
		writeJSONError(writer, http.StatusNotFound, "issue_not_found", "issue identifier is required")
		return
	}
	detail, ok := orchestrator.IssueDetail(identifier)
	if !ok {
		writeJSONError(writer, http.StatusNotFound, "issue_not_found", fmt.Sprintf("issue %s is not tracked", identifier))
		return
	}
	writeJSON(writer, http.StatusOK, detail)
}

func writeJSON(writer http.ResponseWriter, status int, payload any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(payload)
}

func writeJSONError(writer http.ResponseWriter, status int, code string, message string) {
	writeJSON(writer, status, map[string]any{
		"error": map[string]any{
			"code": code,
			"message": message,
		},
	})
}

var dashboardTemplate = template.Must(template.New("dashboard").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Autopilot</title>
  <style>
    body { font-family: Georgia, serif; margin: 2rem; background: #f7f4ec; color: #1f1c17; }
    h1, h2 { margin-bottom: 0.25rem; }
    .meta { color: #5b5144; margin-bottom: 2rem; }
    .cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 1rem; margin-bottom: 2rem; }
    .card { background: #fffaf0; border: 1px solid #d8cfbf; padding: 1rem; border-radius: 12px; }
    table { width: 100%; border-collapse: collapse; margin-bottom: 2rem; background: #fffdf8; }
    th, td { text-align: left; padding: 0.75rem; border-bottom: 1px solid #e3dbc9; vertical-align: top; }
    code { font-family: "SFMono-Regular", Consolas, monospace; font-size: 0.9rem; }
  </style>
</head>
<body>
  <h1>Autopilot Runtime</h1>
  <div class="meta">Generated at {{ .GeneratedAt }}</div>
  <div class="cards">
    <div class="card"><strong>Running</strong><div>{{ .Snapshot.Counts.Running }}</div></div>
    <div class="card"><strong>Retrying</strong><div>{{ .Snapshot.Counts.Retrying }}</div></div>
    <div class="card"><strong>Total Tokens</strong><div>{{ .Snapshot.CopilotTotals.TotalTokens }}</div></div>
    <div class="card"><strong>Runtime Seconds</strong><div>{{ printf "%.1f" .Snapshot.CopilotTotals.SecondsRunning }}</div></div>
  </div>

  <h2>Running Sessions</h2>
  <table>
    <thead>
      <tr><th>Issue</th><th>State</th><th>Session</th><th>Turns</th><th>Last Event</th><th>Tokens</th></tr>
    </thead>
    <tbody>
      {{ range .Snapshot.Running }}
      <tr>
        <td><code>{{ .Identifier }}</code></td>
        <td>{{ .State }}</td>
        <td><code>{{ .SessionID }}</code></td>
        <td>{{ .TurnCount }}</td>
        <td>{{ .LastEvent }} {{ .LastMessage }}</td>
        <td>{{ .Tokens.TotalTokens }}</td>
      </tr>
      {{ else }}
      <tr><td colspan="6">No running sessions.</td></tr>
      {{ end }}
    </tbody>
  </table>

  <h2>Retry Queue</h2>
  <table>
    <thead>
      <tr><th>Issue</th><th>Attempt</th><th>Due At</th><th>Error</th></tr>
    </thead>
    <tbody>
      {{ range .Snapshot.Retrying }}
      <tr>
        <td><code>{{ .Identifier }}</code></td>
        <td>{{ .Attempt }}</td>
        <td>{{ .DueAt.Format "2006-01-02T15:04:05Z07:00" }}</td>
        <td>{{ .Error }}</td>
      </tr>
      {{ else }}
      <tr><td colspan="4">No queued retries.</td></tr>
      {{ end }}
    </tbody>
  </table>
</body>
</html>`))