import "./components/todo-shell.js";
import "./components/task-list.js";

const template = document.createElement("template");
template.innerHTML = `
  <style>
    :host {
      display: block;
      width: min(100%, 42rem);
      color: #18222f;
    }

    .status-note {
      margin: 0;
      padding: 0.9rem 1rem;
      border-radius: 0.85rem;
      border: 1px dashed rgba(24, 34, 47, 0.22);
      background: rgba(255, 255, 255, 0.72);
      color: #314152;
      line-height: 1.5;
    }

    strong {
      display: block;
      margin-bottom: 0.35rem;
      font-size: 0.96rem;
      letter-spacing: 0.01em;
    }
  </style>
  <todo-shell>
    <task-list slot="content"></task-list>
    <p class="status-note" slot="status" data-testid="parallel-note">
      <strong>Minimal starter</strong>
      Add autopilot:ready to the first three foundation issues to let separate
      agents build the shell, list, and state layers from a tiny shared base.
    </p>
  </todo-shell>
`;

export class TodoApp extends HTMLElement {
  connectedCallback() {
    if (this.shadowRoot) {
      return;
    }

    const shadowRoot = this.attachShadow({ mode: "open" });
    shadowRoot.append(template.content.cloneNode(true));
  }
}

if (!customElements.get("todo-app")) {
  customElements.define("todo-app", TodoApp);
}
