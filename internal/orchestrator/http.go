package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Inkbinder/autopilot/internal/runstate"
	"github.com/Inkbinder/autopilot/internal/workflow"
)

const (
	recentRunLimit      = 50
	runDetailPollMS     = 2000
	runDetailReloadText = "Auto-refreshing while run is active"
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
	mux.HandleFunc("/runs", orchestrator.handleRunPageIndex)
	mux.HandleFunc("/runs/", orchestrator.handleRunPage)
	mux.HandleFunc("/", orchestrator.handleDashboard)
	mux.HandleFunc("/api/v1/runs/", orchestrator.handleRun)
	mux.HandleFunc("/api/v1/runs", orchestrator.handleRuns)
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
	recentRuns, err := orchestrator.historyReader().ListRuns(request.Context(), recentRunLimit)
	if err != nil {
		recentRuns = []runstate.RunRecord{}
		orchestrator.logger.Warn("dashboard history load failed", slog.Any("error", err))
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = dashboardTemplate.Execute(writer, map[string]any{
		"Snapshot":        snapshot,
		"RecentRuns":      recentRuns,
		"GeneratedAt":     snapshot.GeneratedAt.Format(time.RFC3339),
		"DashboardPollMS": 2000,
	})
}

func (orchestrator *Orchestrator) handleRunPageIndex(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/runs" {
		http.NotFound(writer, request)
		return
	}
	http.Redirect(writer, request, "/", http.StatusSeeOther)
}

func (orchestrator *Orchestrator) handleRunPage(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	runIDText := strings.Trim(strings.TrimPrefix(request.URL.Path, "/runs/"), "/")
	if runIDText == "" {
		http.Redirect(writer, request, "/", http.StatusSeeOther)
		return
	}
	runID, err := strconv.ParseInt(runIDText, 10, 64)
	if err != nil || runID <= 0 {
		http.NotFound(writer, request)
		return
	}
	detail, ok, err := orchestrator.historyReader().GetRun(request.Context(), runID)
	if err != nil {
		orchestrator.logger.Warn("run detail page fetch failed", slog.Int64("run_id", runID), slog.Any("error", err))
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte("run history is unavailable"))
		return
	}
	if !ok {
		http.NotFound(writer, request)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = runDetailTemplate.Execute(writer, buildRunDetailPageData(detail))
}

func (orchestrator *Orchestrator) handleRuns(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writeJSONError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	runs, err := orchestrator.historyReader().ListRuns(request.Context(), recentRunLimit)
	if err != nil {
		orchestrator.logger.Warn("run history fetch failed", slog.Any("error", err))
		writeJSONError(writer, http.StatusInternalServerError, "history_unavailable", "run history is unavailable")
		return
	}
	writeJSON(writer, http.StatusOK, runs)
}

func (orchestrator *Orchestrator) handleRun(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writeJSONError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	runIDText := strings.Trim(strings.TrimPrefix(request.URL.Path, "/api/v1/runs/"), "/")
	if runIDText == "" {
		orchestrator.handleRuns(writer, request)
		return
	}
	runID, err := strconv.ParseInt(runIDText, 10, 64)
	if err != nil || runID <= 0 {
		writeJSONError(writer, http.StatusBadRequest, "invalid_run_id", "run id must be a positive integer")
		return
	}
	detail, ok, err := orchestrator.historyReader().GetRun(request.Context(), runID)
	if err != nil {
		orchestrator.logger.Warn("run detail fetch failed", slog.Int64("run_id", runID), slog.Any("error", err))
		writeJSONError(writer, http.StatusInternalServerError, "history_unavailable", "run history is unavailable")
		return
	}
	if !ok {
		writeJSONError(writer, http.StatusNotFound, "run_not_found", fmt.Sprintf("run %d was not found", runID))
		return
	}
	writeJSON(writer, http.StatusOK, detail)
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
		.section-copy { margin: 0 0 1rem; color: #5b5144; }
		a.run-link { color: #6d3c1f; text-decoration: none; }
		a.run-link:hover { text-decoration: underline; }
		.status-pill { display: inline-flex; align-items: center; border-radius: 999px; padding: 0.18rem 0.55rem; font-size: 0.84rem; border: 1px solid #d8cfbf; background: #f4ecdd; text-transform: lowercase; }
		.status-running { background: #ecf5e7; border-color: #b9d7aa; }
		.status-success { background: #edf6f0; border-color: #b5d2bf; }
		.status-failed { background: #f9e9e4; border-color: #e0b7aa; }
		.status-queued { background: #f3efe5; border-color: #d8cfbf; }
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
				<td>{{ if gt .RunID 0 }}<a class="run-link" href="/runs/{{ .RunID }}"><code>{{ .Identifier }}</code></a>{{ else }}<code>{{ .Identifier }}</code>{{ end }}</td>
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

	<h2>History</h2>
	<p class="section-copy">Recent run attempts from the local SQLite state store. Open a run to inspect prompts, responses, and shell output on its own page.</p>
	<table>
		<thead>
			<tr><th>Run</th><th>Repo</th><th>Issue ID</th><th>Status</th><th>Started</th><th>Ended</th><th>Error</th></tr>
		</thead>
		<tbody id="history-body">
			{{ range .RecentRuns }}
			<tr>
				<td><a class="run-link" href="/runs/{{ .ID }}">#{{ .ID }}</a></td>
				<td><code>{{ .Repo }}</code></td>
				<td><a class="run-link" href="/runs/{{ .ID }}"><code>{{ .IssueID }}</code></a></td>
				<td><span class="status-pill status-{{ .Status }}">{{ .Status }}</span></td>
				<td>{{ .StartTime.Format "2006-01-02T15:04:05Z07:00" }}</td>
				<td>{{ if .EndTime }}{{ .EndTime.Format "2006-01-02T15:04:05Z07:00" }}{{ else }}<span class="muted">active</span>{{ end }}</td>
				<td>{{ if .ErrorMessage }}{{ .ErrorMessage }}{{ end }}</td>
			</tr>
			{{ else }}
			<tr><td colspan="7">No historical runs yet.</td></tr>
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
					var issueCell = item.run_id
						? '<a class="run-link" href="/runs/' + encodeURIComponent(item.run_id) + '"><code>' + escapeHtml(item.issue_identifier || "") + '</code></a>'
						: '<code>' + escapeHtml(item.issue_identifier || "") + '</code>';
					return [
						'<tr>',
						'<td>' + issueCell + '</td>',
						'<td>' + escapeHtml(item.state || "") + '</td>',
						'<td><code>' + escapeHtml(item.session_id || "") + '</code></td>',
						'<td>' + escapeHtml(item.turn_count || 0) + '</td>',
						'<td>' + escapeHtml(lastEvent) + '</td>',
						'<td>' + escapeHtml((item.tokens && item.tokens.total_tokens) || 0) + '</td>',
						'</tr>'
					].join("");
				}).join("");
			}

			function statusClass(status) {
				return 'status-pill status-' + escapeHtml(status || 'queued');
			}

			function renderHistoryRows(items) {
				if (!items.length) {
					return '<tr><td colspan="7">No historical runs yet.</td></tr>';
				}
				return items.map(function(item) {
					return [
						'<tr>',
						'<td><a class="run-link" href="/runs/' + encodeURIComponent(item.id) + '">#' + escapeHtml(item.id) + '</a></td>',
						'<td><code>' + escapeHtml(item.repo || '') + '</code></td>',
						'<td><a class="run-link" href="/runs/' + encodeURIComponent(item.id) + '"><code>' + escapeHtml(item.issue_id || '') + '</code></a></td>',
						'<td><span class="' + statusClass(item.status) + '">' + escapeHtml(item.status || '') + '</span></td>',
						'<td>' + escapeHtml(formatTimestamp(item.start_time)) + '</td>',
						'<td>' + (item.end_time ? escapeHtml(formatTimestamp(item.end_time)) : '<span class="muted">active</span>') + '</td>',
						'<td>' + escapeHtml(item.error_message || '') + '</td>',
						'</tr>'
					].join('');
				}).join('');
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

			function renderHistory(runs) {
				var historyBody = document.getElementById('history-body');
				if (historyBody) {
					historyBody.innerHTML = renderHistoryRows(runs || []);
				}
			}

			async function refreshDashboard() {
				try {
					var responses = await Promise.all([
						fetch('/api/v1/state', { cache: 'no-store' }),
						fetch('/api/v1/runs', { cache: 'no-store' })
					]);
					if (!responses[0].ok) {
						throw new Error('state request failed with status ' + responses[0].status);
					}
					if (!responses[1].ok) {
						throw new Error('history request failed with status ' + responses[1].status);
					}
					var snapshot = await responses[0].json();
					var runs = await responses[1].json();
					renderSnapshot(snapshot);
					renderHistory(runs);
				} catch (error) {
					var refreshError = document.getElementById("refresh-error");
					if (refreshError) {
						refreshError.hidden = false;
						refreshError.textContent = "Auto-refresh paused: " + error.message;
					}
				}
			}

			refreshDashboard();
			window.setInterval(refreshDashboard, pollIntervalMs);
		})();
	</script>
</body>
</html>`))

var runDetailTemplate = template.Must(template.New("run-detail").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Run #{{ .RunID }}</title>
  <style>
    body { font-family: Georgia, serif; margin: 2rem; background: #f7f4ec; color: #1f1c17; }
    a { color: #6d3c1f; text-decoration: none; }
    a:hover { text-decoration: underline; }
    h1, h2 { margin-bottom: 0.3rem; }
    .meta { color: #5b5144; margin-bottom: 1rem; display: flex; gap: 0.9rem; flex-wrap: wrap; align-items: center; }
		.badge { display: inline-flex; align-items: center; gap: 0.4rem; padding: 0.35rem 0.65rem; border-radius: 999px; background: #efe6d4; border: 1px solid #d8cfbf; font-size: 0.9rem; }
		.badge::before { content: ""; width: 0.55rem; height: 0.55rem; border-radius: 999px; background: #0e8a16; }
    .status-pill { display: inline-flex; align-items: center; border-radius: 999px; padding: 0.18rem 0.55rem; font-size: 0.84rem; border: 1px solid #d8cfbf; background: #f4ecdd; text-transform: lowercase; }
    .status-running { background: #ecf5e7; border-color: #b9d7aa; }
    .status-success { background: #edf6f0; border-color: #b5d2bf; }
    .status-failed { background: #f9e9e4; border-color: #e0b7aa; }
    .status-queued { background: #f3efe5; border-color: #d8cfbf; }
    .summary { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 1rem; margin: 1.5rem 0; }
    .card { background: #fffaf0; border: 1px solid #d8cfbf; padding: 1rem; border-radius: 12px; }
    .timeline { display: grid; gap: 1rem; }
    .event { background: #fffdf8; border: 1px solid #e3dbc9; border-radius: 12px; padding: 1rem; }
    .event-head { display: flex; gap: 0.75rem; align-items: center; flex-wrap: wrap; margin-bottom: 0.5rem; }
    .action-chip { display: inline-flex; align-items: center; border-radius: 999px; padding: 0.12rem 0.5rem; background: #efe6d4; border: 1px solid #d8cfbf; font-size: 0.82rem; }
    .muted { color: #7b7062; }
    .section { margin-top: 0.8rem; }
    .section strong { display: block; margin-bottom: 0.3rem; }
    pre { white-space: pre-wrap; word-break: break-word; background: #f6f0e4; border: 1px solid #e3dbc9; padding: 0.75rem; border-radius: 10px; margin: 0; overflow-x: auto; }
    details { margin-top: 0.8rem; }
    summary { cursor: pointer; color: #6d3c1f; }
    .empty-state { color: #7b7062; background: #fffdf8; border: 1px solid #e3dbc9; border-radius: 12px; padding: 1rem; }
  </style>
</head>
<body>
  <div class="meta">
    <a href="/">Back to Dashboard</a>
    <span class="muted">Generated at {{ .GeneratedAt }}</span>
		{{ if .AutoRefreshMS }}<span class="badge">{{ .AutoRefreshText }} every {{ .AutoRefreshMS }}ms</span>{{ end }}
  </div>
  <h1>Run #{{ .RunID }}</h1>
  <div class="meta">
    <span><strong>Status:</strong> <span class="status-pill status-{{ .Status }}">{{ .Status }}</span></span>
    <span><strong>Repo:</strong> <code>{{ .Repo }}</code></span>
    <span><strong>Issue ID:</strong> <code>{{ .IssueID }}</code></span>
  </div>

  <div class="summary">
    <div class="card"><strong>Started</strong><div>{{ .StartTime }}</div></div>
    <div class="card"><strong>Ended</strong><div>{{ .EndTime }}</div></div>
    <div class="card"><strong>Error</strong><div>{{ .ErrorMessage }}</div></div>
  </div>

  <h2>Timeline</h2>
  {{ if .AuditEvents }}
  <div class="timeline">
    {{ range .AuditEvents }}
    <article class="event">
      <div class="event-head">
        <span class="action-chip">{{ .ActionType }}</span>
        <span class="muted">{{ .Timestamp }}</span>
      </div>
      {{ if .Summary }}<div>{{ .Summary }}</div>{{ end }}
      {{ range .Sections }}
      <div class="section">
        <strong>{{ .Title }}</strong>
        <pre>{{ .Content }}</pre>
      </div>
      {{ end }}
      {{ if .RawPayload }}
      <details>
        <summary>Raw Payload</summary>
        <pre>{{ .RawPayload }}</pre>
      </details>
      {{ end }}
    </article>
    {{ end }}
  </div>
  {{ else }}
  <div class="empty-state">No audit events recorded for this run.</div>
  {{ end }}
	{{ if .AutoRefreshMS }}
	<script>
		window.setTimeout(function() {
			window.location.reload();
		}, {{ .AutoRefreshMS }});
	</script>
	{{ end }}
</body>
</html>`))

type runDetailPageData struct {
	RunID           int64
	Repo            string
	IssueID         string
	Status          string
	StartTime       string
	EndTime         string
	ErrorMessage    string
	GeneratedAt     string
	AutoRefreshMS   int
	AutoRefreshText string
	AuditEvents     []runEventView
}

type runEventView struct {
	ActionType string
	Timestamp  string
	Summary    string
	Sections   []runEventSection
	RawPayload string
}

type runEventSection struct {
	Title   string
	Content string
}

func buildRunDetailPageData(detail runstate.RunDetail) runDetailPageData {
	page := runDetailPageData{
		RunID:           detail.ID,
		Repo:            detail.Repo,
		IssueID:         detail.IssueID,
		Status:          string(detail.Status),
		StartTime:       detail.StartTime.Format(time.RFC3339),
		EndTime:         "active",
		ErrorMessage:    firstNonEmpty(stringPtrValue(detail.ErrorMessage), "none"),
		GeneratedAt:     time.Now().UTC().Format(time.RFC3339),
		AutoRefreshText: runDetailReloadText,
		AuditEvents:     make([]runEventView, 0, len(detail.AuditEvents)),
	}
	if detail.EndTime != nil {
		page.EndTime = detail.EndTime.Format(time.RFC3339)
	}
	if detail.Status == runstate.StatusQueued || detail.Status == runstate.StatusRunning {
		page.AutoRefreshMS = runDetailPollMS
	}
	for _, event := range detail.AuditEvents {
		page.AuditEvents = append(page.AuditEvents, buildRunEventView(event))
	}
	return page
}

func buildRunEventView(event runstate.AuditEventRecord) runEventView {
	payload := decodeAuditPayload(event.Payload)
	sections, summary := describeAuditPayload(event.ActionType, payload)
	return runEventView{
		ActionType: event.ActionType,
		Timestamp:  event.Timestamp.Format(time.RFC3339),
		Summary:    summary,
		Sections:   sections,
		RawPayload: prettyJSON(event.Payload),
	}
}

func describeAuditPayload(actionType string, payload any) ([]runEventSection, string) {
	payloadMap, _ := payload.(map[string]any)
	sections := make([]runEventSection, 0)
	summary := actionType
	switch actionType {
	case "llm_prompt":
		summary = fmt.Sprintf("Prompt turn %s", nonEmptyString(stringValue(payloadMap, "turn"), "?"))
		sections = appendSection(sections, "Prompt", payloadValue(payloadMap, "prompt"))
		sections = appendSection(sections, "Request", payloadValue(payloadMap, "request"))
		sections = appendSection(sections, "Response", payloadValue(payloadMap, "response"))
		sections = appendSection(sections, "Response Error", payloadValue(payloadMap, "response_error"))
	case "workspace_exec":
		summary = commandSummary(payloadMap)
		sections = appendSection(sections, "Output", payloadValue(payloadMap, "output"))
		sections = appendSection(sections, "Error", payloadValue(payloadMap, "error"))
	case "copilot_cli_start":
		summary = nonEmptyString(stringValue(payloadMap, "command"), "copilot process start")
	case "copilot_stderr":
		summary = "Copilot stderr"
		sections = appendSection(sections, "Message", payloadValue(payloadMap, "message"))
	default:
		if payloadMap != nil {
			sections = appendSection(sections, "Payload", payload)
		}
	}
	return sections, summary
}

func decodeAuditPayload(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return string(raw)
	}
	return payload
}

func prettyJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	buffer := &bytes.Buffer{}
	if err := json.Indent(buffer, raw, "", "  "); err == nil {
		return buffer.String()
	}
	return string(raw)
}

func appendSection(sections []runEventSection, title string, value any) []runEventSection {
	formatted := formatValue(value)
	if formatted == "" {
		return sections
	}
	return append(sections, runEventSection{Title: title, Content: formatted})
}

func formatValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	default:
		encoded, err := json.MarshalIndent(typed, "", "  ")
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(encoded)
	}
}

func commandSummary(payload map[string]any) string {
	if payload == nil {
		return "workspace command"
	}
	command := strings.TrimSpace(stringValue(payload, "command"))
	argsValue, _ := payload["args"].([]any)
	args := make([]string, 0, len(argsValue))
	for _, value := range argsValue {
		args = append(args, strings.TrimSpace(fmt.Sprint(value)))
	}
	combined := strings.TrimSpace(strings.Join(append([]string{command}, args...), " "))
	return nonEmptyString(combined, "workspace command")
}

func payloadValue(values map[string]any, key string) any {
	if values == nil {
		return nil
	}
	return values[key]
}

func stringValue(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, ok := values[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func stringPtrValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func nonEmptyString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
