import "./task-item.js";

const template = document.createElement("template");
template.innerHTML = `
  <style>
    :host {
      display: block;
    }
  </style>
  <div data-testid="list-placeholder"></div>
`;

export class TaskList extends HTMLElement {
  connectedCallback() {
    if (this.shadowRoot) {
      return;
    }

    const shadowRoot = this.attachShadow({ mode: "open" });
    shadowRoot.append(template.content.cloneNode(true));
  }
}

if (!customElements.get("task-list")) {
  customElements.define("task-list", TaskList);
}