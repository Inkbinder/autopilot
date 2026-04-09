const template = document.createElement("template");
template.innerHTML = `
  <span hidden></span>
`;

export class TaskItem extends HTMLElement {
  connectedCallback() {
    if (this.shadowRoot) {
      return;
    }

    const shadowRoot = this.attachShadow({ mode: "open" });
    shadowRoot.append(template.content.cloneNode(true));
  }
}

if (!customElements.get("task-item")) {
  customElements.define("task-item", TaskItem);
}