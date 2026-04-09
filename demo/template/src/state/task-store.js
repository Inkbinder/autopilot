export const TASK_STORAGE_KEY = "autopilot-demo-tasks";

const defaultStorage = typeof globalThis !== "undefined" ? globalThis.localStorage ?? null : null;

export class TaskStore {
  constructor({ storage = defaultStorage, storageKey = TASK_STORAGE_KEY } = {}) {
    this.storage = storage;
    this.storageKey = storageKey;
  }

  createInitialState() {
    return {
      filter: "all",
      tasks: []
    };
  }
}