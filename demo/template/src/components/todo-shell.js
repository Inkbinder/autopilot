const template = document.createElement("template");
template.innerHTML = `
  <style>
    :host {
      display: block;
      color: #18222f;
    }
  </style>
  <slot name="composer"></slot>
  <slot name="content"></slot>
  <slot name="status"></slot>
`;

export class TodoShell extends HTMLElement {
  connectedCallback() {
    if (this.shadowRoot) {
      return;
    }

    const shadowRoot = this.attachShadow({ mode: "open" });
    shadowRoot.append(template.content.cloneNode(true));
  }
}

if (!customElements.get("todo-shell")) {
  customElements.define("todo-shell", TodoShell);
}