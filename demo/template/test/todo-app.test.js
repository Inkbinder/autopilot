import { afterEach, describe, expect, it } from "vitest";

import "../src/todo-app.js";

describe("todo-app", () => {
  afterEach(() => {
    document.body.innerHTML = "";
  });

  it("renders the starter shell", () => {
    document.body.innerHTML = "<todo-app></todo-app>";

    const element = document.querySelector("todo-app");
    const shadowRoot = element?.shadowRoot;

    expect(shadowRoot?.querySelector("todo-shell")).not.toBeNull();
    expect(shadowRoot?.querySelector("task-list")).not.toBeNull();
    expect(shadowRoot?.querySelector("[data-testid='parallel-note']")?.textContent).toContain("tiny shared base");
  });
});
