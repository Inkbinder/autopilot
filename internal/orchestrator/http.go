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

	"github.com/Inkbinder/autopilot/internal/workflow"
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
		"Snapshot":         snapshot,
		"GeneratedAt":      snapshot.GeneratedAt.Format(time.RFC3339),
		"DashboardPollMS": 2000,
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
		"queued":       true,
		"coalesced":    coalesced,
		"requested_at": time.Now().UTC().Format(time.RFC3339),
		"operations":   []string{"poll", "reconcile"},
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
			"code":    code,
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
		.meta { color: #5b5144; margin-bottom: 1rem; display: flex; gap: 1rem; flex-wrap: wrap; align-items: center; }
		.badge { display: inline-flex; align-items: center; gap: 0.4rem; padding: 0.35rem 0.65rem; border-radius: 999px; background: #efe6d4; border: 1px solid #d8cfbf; font-size: 0.9rem; }
		.badge::before { content: ""; width: 0.55rem; height: 0.55rem; border-radius: 999px; background: #0e8a16; }
    .cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 1rem; margin-bottom: 2rem; }
    .card { background: #fffaf0; border: 1px solid #d8cfbf; padding: 1rem; border-radius: 12px; }
    table { width: 100%; border-collapse: collapse; margin-bottom: 2rem; background: #fffdf8; }
    th, td { text-align: left; padding: 0.75rem; border-bottom: 1px solid #e3dbc9; vertical-align: top; }
    code { font-family: "SFMono-Regular", Consolas, monospace; font-size: 0.9rem; }
		.muted { color: #7b7062; }
  </style>
</head>
<body>
  <h1>Autopilot Runtime</h1>
	<div class="meta">
		<div id="generated-at">Generated at {{ .GeneratedAt }}</div>
		<div class="badge">Auto-refreshing every {{ .DashboardPollMS }}ms</div>
		<div id="refresh-error" class="muted" hidden></div>
	</div>
  <div class="cards">
		<div class="card"><strong>Running</strong><div id="count-running">{{ .Snapshot.Counts.Running }}</div></div>
		<div class="card"><strong>Retrying</strong><div id="count-retrying">{{ .Snapshot.Counts.Retrying }}</div></div>
		<div class="card"><strong>Total Tokens</strong><div id="total-tokens">{{ .Snapshot.CopilotTotals.TotalTokens }}</div></div>
		<div class="card"><strong>Runtime Seconds</strong><div id="runtime-seconds">{{ printf "%.1f" .Snapshot.CopilotTotals.SecondsRunning }}</div></div>
  </div>

  <h2>Running Sessions</h2>
  <table>
    <thead>
      <tr><th>Issue</th><th>State</th><th>Session</th><th>Turns</th><th>Last Event</th><th>Tokens</th></tr>
    </thead>
		<tbody id="running-body">
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
		<tbody id="retrying-body">
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
	<script>
		(function() {
			var pollIntervalMs = {{ .DashboardPollMS }};

			function escapeHtml(value) {
				return String(value)
					.replace(/&/g, "&amp;")
					.replace(/</g, "&lt;")
					.replace(/>/g, "&gt;")
					.replace(/\"/g, "&quot;")
					.replace(/'/g, "&#39;");
			}

			function formatTimestamp(value) {
				if (!value) {
					return "";
				}
				var date = new Date(value);
				if (Number.isNaN(date.getTime())) {
					return value;
				}
				return date.toISOString();
			}

			function setText(id, value) {
				var element = document.getElementById(id);
				if (element) {
					element.textContent = value;
				}
			}

			function renderRunningRows(items) {
				if (!items.length) {
					return '<tr><td colspan="6">No running sessions.</td></tr>';
				}
				return items.map(function(item) {
					var lastEvent = [item.last_event || "", item.last_message || ""].join(" ").trim();
					return [
						'<tr>',
						'<td><code>' + escapeHtml(item.issue_identifier || "") + '</code></td>',
						'<td>' + escapeHtml(item.state || "") + '</td>',
						'<td><code>' + escapeHtml(item.session_id || "") + '</code></td>',
						'<td>' + escapeHtml(item.turn_count || 0) + '</td>',
						'<td>' + escapeHtml(lastEvent) + '</td>',
						'<td>' + escapeHtml((item.tokens && item.tokens.total_tokens) || 0) + '</td>',
						'</tr>'
					].join("");
				}).join("");
			}

			function renderRetryRows(items) {
				if (!items.length) {
					return '<tr><td colspan="4">No queued retries.</td></tr>';
				}
				return items.map(function(item) {
					return [
						'<tr>',
						'<td><code>' + escapeHtml(item.issue_identifier || "") + '</code></td>',
						'<td>' + escapeHtml(item.attempt || 0) + '</td>',
						'<td>' + escapeHtml(formatTimestamp(item.due_at)) + '</td>',
						'<td>' + escapeHtml(item.error || "") + '</td>',
						'</tr>'
					].join("");
				}).join("");
			}

			function renderSnapshot(snapshot) {
				setText("generated-at", "Generated at " + formatTimestamp(snapshot.generated_at));
				setText("count-running", snapshot.counts ? snapshot.counts.running : 0);
				setText("count-retrying", snapshot.counts ? snapshot.counts.retrying : 0);
				setText("total-tokens", snapshot.copilot_totals ? snapshot.copilot_totals.total_tokens : 0);
				setText("runtime-seconds", snapshot.copilot_totals ? Number(snapshot.copilot_totals.seconds_running || 0).toFixed(1) : "0.0");

				var runningBody = document.getElementById("running-body");
				if (runningBody) {
					runningBody.innerHTML = renderRunningRows(snapshot.running || []);
				}

				var retryingBody = document.getElementById("retrying-body");
				if (retryingBody) {
					retryingBody.innerHTML = renderRetryRows(snapshot.retrying || []);
				}

				var refreshError = document.getElementById("refresh-error");
				if (refreshError) {
					refreshError.hidden = true;
					refreshError.textContent = "";
				}
			}

			async function refreshDashboard() {
				try {
					var response = await fetch("/api/v1/state", { cache: "no-store" });
					if (!response.ok) {
						throw new Error("state request failed with status " + response.status);
					}
					var snapshot = await response.json();
					renderSnapshot(snapshot);
				} catch (error) {
					var refreshError = document.getElementById("refresh-error");
					if (refreshError) {
						refreshError.hidden = false;
						refreshError.textContent = "Auto-refresh paused: " + error.message;
					}
				}
			}

			window.setInterval(refreshDashboard, pollIntervalMs);
		})();
	</script>
</body>
</html>`))
