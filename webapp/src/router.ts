import { createRouter, createWebHistory, type RouteRecordRaw, type RouterHistory } from "vue-router";
import type { Component } from "vue";
import Placeholder from "./components/Placeholder.vue";
import MetricDetailView from "./views/MetricDetailView.vue";
import OverviewView from "./views/OverviewView.vue";
import AlertsView from "./views/AlertsView.vue";
import KeysView from "./views/KeysView.vue";
import SessionsView from "./views/SessionsView.vue";
import UsageView from "./views/UsageView.vue";
import UsersView from "./views/UsersView.vue";

export type Role = "admin" | "user";

// Tabs only administrators open; every other route is open to everyone.
const adminOnly = new Set(["overview", "users", "alerts"]);

/** landing is the first tab of a role. */
export function landing(role: Role): string {
  return role === "admin" ? "/overview" : "/usage";
}

/** allowed reports whether a role may open a path; a detail view follows its parent tab. */
export function allowed(path: string, role: Role): boolean {
  if (role === "admin") return true;
  return !adminOnly.has(path.split("/")[1] ?? "");
}

const tab = (path: string, title: string, component: Component = Placeholder): RouteRecordRaw => ({
  path,
  component,
  meta: { title },
});

export const routes: RouteRecordRaw[] = [
  tab("/", ""),
  tab("/overview", "Overview", OverviewView),
  tab("/overview/:metric", "Overview", MetricDetailView),
  tab("/usage", "Usage", UsageView),
  tab("/usage/:metric", "Usage", MetricDetailView),
  tab("/sessions", "Sessions", SessionsView),
  tab("/keys", "Keys", KeysView),
  tab("/users", "Users", UsersView),
  tab("/alerts", "Alerts", AlertsView),
  // The device page replaces the shell; the shell renders it.
  { path: "/device", component: Placeholder, meta: { bare: true } },
  // Unknown paths go to "/", where the shell sends the caller to their landing tab.
  { path: "/:p(.*)*", redirect: "/" },
];

export function createAppRouter(history: RouterHistory = createWebHistory()) {
  return createRouter({ history, routes });
}
