import { useCallback, useEffect, useState } from "react";
import { api } from "../api";
import { useLocale } from "../i18n";
import type { AuditEvent, Permission } from "../types";
import { Avatar, Badge, Empty, ErrorNote, Loading, useTopBar } from "../ui";

const tones: Record<string, "" | "ok" | "accent" | "warn" | "danger"> = {
  "grant.set": "", "grant.remove": "warn", "user.create": "ok", "user.provision": "ok", "user.update": "accent",
  "user.disable": "danger", "user.directory_disable": "danger", "user.enable": "ok", "user.directory_enable": "ok", "repository.create": "ok",
  "mcp.token.create": "accent", "mcp.token.revoke": "warn", "mcp.download": "accent",
};

export function AuditPage() {
  const { t, dateTime, ago, error } = useLocale();
  const [events, setEvents] = useState<AuditEvent[] | null>(null);
  const [cursor, setCursor] = useState("");
  const [problem, setProblem] = useState("");
  const [loading, setLoading] = useState(false);
  useTopBar({ title: t("nav.audit"), description: t("audit.description") }, [t]);

  const load = useCallback(async (after: string) => {
    setLoading(true);
    try {
      const page = await api.audit(after);
      setEvents((current) => (after && current ? [...current, ...page.items] : page.items));
      setCursor(page.nextCursor ?? "");
      setProblem("");
    } catch (cause) {
      setProblem(error(cause));
    } finally {
      setLoading(false);
    }
  }, [error]);
  useEffect(() => { void load(""); }, [load]);

  const permission = (value?: string) => (value ? t(`permission.${value as Permission}`) : t("audit.noAccess"));
  const subject = (event: AuditEvent) => event.subjectType === "anonymous" ? t("audit.anyone")
    : event.subjectType === "authenticated" ? t("audit.anySignedIn")
    : event.subjectName || event.subject || "";
  const change = (event: AuditEvent) => {
    switch (event.action) {
      case "grant.set":
      case "grant.remove":
        return `${subject(event)}: ${permission(event.before)} → ${permission(event.after)}`;
      case "user.update":
        return `${event.before === "admin" ? t("users.roleAdmin") : t("users.roleUser")} → ${event.after === "admin" ? t("users.roleAdmin") : t("users.roleUser")}`;
      case "repository.update":
        return event.before && event.before !== event.after ? t("audit.renamed", { name: event.before }) : t("audit.details");
      case "user.supersede":
        return t("audit.superseded", { name: event.before ?? "" });
      case "settings.update":
        return settingsChange(event.before, event.after);
      case "lock.release":
        return t("audit.lockReleased", { path: event.before ?? "", owner: subject(event) });
      case "user.mcp":
      case "access.mcp": {
        const state = (value?: string) => (value === "off" ? t("audit.off") : t("audit.on"));
        const change = t("audit.mcpSwitch", { before: state(event.before), after: state(event.after) });
        return event.action === "access.mcp" && subject(event) ? `${subject(event)}: ${change}` : change;
      }
      case "mcp.token.create":
        return event.after || "—";
      case "mcp.token.revoke":
        return event.before || "—";
      case "mcp.download":
        return event.after || "—";
      default:
        return "—";
    }
  };
  // Settings events store "registration:theme:language" before and after.
  const settingsChange = (before = "", after = "") => {
    const [registrationBefore, themeBefore, languageBefore] = before.split(":");
    const [registration, theme, language] = after.split(":");
    const parts: string[] = [];
    if (registration && registration !== registrationBefore) parts.push(`${t("settings.registration")}: ${registration === "true" ? t("audit.registrationOn") : t("audit.registrationOff")}`);
    if (theme && theme !== themeBefore) parts.push(`${t("settings.theme")}: ${themeBefore || "—"} → ${theme}`);
    if (language && language !== languageBefore) parts.push(`${t("settings.language")}: ${(languageBefore || "—").toUpperCase()} → ${language.toUpperCase()}`);
    return parts.join(" · ") || "—";
  };
  const actionLabel = (action: string) => {
    const key = `action.${action}` as Parameters<typeof t>[0];
    const text = t(key);
    return text === key ? action : text;
  };
  if (problem) return <ErrorNote message={problem} onRetry={() => void load("")} />;
  if (!events) return <Loading />;
  if (events.length === 0) return <div className="card"><Empty icon="audit" title={t("audit.empty")} /></div>;
  return <div className="card table-wrap">
    <table className="table stacked"><thead><tr><th>{t("audit.time")}</th><th>{t("audit.actor")}</th><th>{t("audit.action")}</th><th>{t("audit.target")}</th><th className="opt">{t("audit.change")}</th></tr></thead>
      <tbody>{events.map((event) => {
        const actor = event.actorId ? event.actorName || t("audit.formerUser") : t("audit.system");
        return <tr key={event.id}>
          <td className="muted nowrap" title={dateTime(event.createdAt)}>{ago(event.createdAt)}</td>
          <td><div className="row"><Avatar name={actor} />{actor}</div></td>
          <td><Badge kind={tones[event.action] ?? ""}>{actionLabel(event.action)}</Badge></td>
          <td><b>{event.targetType === "settings" ? t("nav.settings") : event.targetName || (event.targetType === "repository" ? t("audit.formerRepository") : event.targetType === "user" ? t("audit.formerUser") : event.targetId)}</b></td>
          <td className="muted opt">{change(event)}</td>
        </tr>;
      })}</tbody></table>
    {cursor && <div className="table-foot"><span /><button className="btn sm" disabled={loading} onClick={() => void load(cursor)}>{t("audit.older")}</button></div>}
  </div>;
}
