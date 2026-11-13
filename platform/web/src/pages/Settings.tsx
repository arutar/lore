import { useEffect, useState } from "react";
import { api } from "../api";
import { useApp } from "../App";
import { useLocale } from "../i18n";
import type { GlobalSettings, Language } from "../types";
import { Badge, ErrorNote, Icon, Loading, Switch, useToast, useTopBar } from "../ui";

/** Preview colours of each theme: background, surface, accent, text. */
const themes: [string, string, string, string, string][] = [
  ["midnight", "Midnight", "#0d1117", "#1e2630", "#58a6ff"], ["sky", "Sky", "#2b2825", "#403b36", "#4f9fe3"], ["ember", "Ember", "#1c1a1a", "#3b3333", "#d32b2b"],
  ["sage", "Sage", "#292c27", "#43483e", "#f1926f"], ["aurora", "Aurora", "#100e1c", "#2f2954", "#b7a0ff"], ["ocean", "Ocean", "#0b1d2f", "#17395a", "#8fc9ff"],
  ["retro", "Retro", "#181714", "#36332a", "#f4ce81"], ["autumn", "Autumn", "#1b1816", "#3b332d", "#f7946a"], ["light", "Light", "#ece7da", "#e3dccb", "#b8460f"],
  ["cyberpunk", "Cyberpunk", "#0b0915", "#2d2654", "#68e7f6"],
];
const languages: [Language, string, string][] = [["ru", "Русский", "Репозитории · Пользователи · Настройки"], ["en", "English", "Repositories · Users · Settings"]];

export function SettingsPage() {
  const { t, error } = useLocale();
  const { config, setConfig } = useApp();
  const toast = useToast();
  const [saved, setSaved] = useState<GlobalSettings | null>(null);
  const [draft, setDraft] = useState<GlobalSettings | null>(null);
  const [problem, setProblem] = useState("");
  const [saving, setSaving] = useState(false);
  useTopBar({ title: t("nav.settings"), description: t("settings.description") }, [t]);
  useEffect(() => { api.settings().then((settings) => { setSaved(settings); setDraft(settings); }).catch((cause) => setProblem(error(cause))); }, [error]);
  // Preview the theme at once; leaving without saving restores the saved one.
  useEffect(() => {
    if (draft) document.documentElement.dataset.theme = draft.theme;
    return () => { document.documentElement.dataset.theme = config.theme; };
  }, [draft?.theme, config.theme]);

  if (problem) return <ErrorNote message={problem} />;
  if (!draft || !saved) return <Loading />;
  const dirty = draft.theme !== saved.theme || draft.language !== saved.language || draft.registrationEnabled !== saved.registrationEnabled;
  const save = async () => {
    setSaving(true);
    try {
      const result = await api.saveSettings(draft);
      setSaved(result);
      setDraft(result);
      setConfig({ ...config, theme: result.theme, language: result.language, registrationEnabled: result.registrationEnabled });
      toast(t("settings.saved"));
    } catch (cause) {
      toast(error(cause), { error: true });
    } finally {
      setSaving(false);
    }
  };
  const saveButton = <button className="btn primary sm" disabled={!dirty || saving} onClick={() => void save()}>{t("settings.save")}</button>;
  return <div className="stack">
    <div className="card">
      <div className="card-head"><div className="grow"><h2>{t("settings.theme")}</h2><p>{t("settings.themeText")}</p></div>{saveButton}</div>
      <div className="pad"><div className="theme-grid">{themes.map(([id, name, background, surface, accent]) =>
        <button key={id} className={`theme ${draft.theme === id ? "on" : ""}`} aria-pressed={draft.theme === id} onClick={() => setDraft({ ...draft, theme: id })}>
          <span className="prev" style={{ background }}><i style={{ width: "55%", background: id === "light" ? "#1e2a25" : "#f2ead8" }} /><i style={{ width: "80%", background: surface }} /><i style={{ width: "34%", background: accent }} /></span>
          <b>{name}{draft.theme === id && <Icon name="check" />}</b></button>)}</div></div>
    </div>
    <div className="card">
      <div className="card-head"><div className="grow"><h2>{t("settings.language")}</h2><p>{t("settings.languageText")}</p></div>{saveButton}</div>
      <div className="pad"><div className="theme-grid">{languages.map(([id, name, sample]) =>
        <button key={id} className={`theme ${draft.language === id ? "on" : ""}`} aria-pressed={draft.language === id} onClick={() => setDraft({ ...draft, language: id })}>
          <b>{name}{draft.language === id && <Icon name="check" />}</b><span className="muted small">{sample}</span></button>)}</div></div>
    </div>
    <div className="card">
      <div className="card-head"><div className="grow"><h2>{t("settings.registration")}</h2></div>{saveButton}</div>
      <div className="setting-row"><div className="grow"><p>{t("settings.registrationText")}</p></div>
        <Switch checked={draft.registrationEnabled} onChange={(on) => setDraft({ ...draft, registrationEnabled: on })} label={t("settings.registration")} /></div>
      {!draft.registrationEnabled && <div className="setting-row"><div className="note grow"><Icon name="info" /><span>{t("settings.registrationClosed")}</span></div></div>}
    </div>
    <div className="card">
      <div className="card-head"><div className="grow"><h2>{t("settings.server")}</h2></div></div>
      <div className="status-list">
        <div><Icon name="shield" className="file-ico" /><div className="grow"><h3>{t("settings.oidc")}</h3></div><Badge kind={config.oidcEnabled ? "ok" : ""}>{config.oidcEnabled ? t("settings.enabled") : t("settings.disabled")}</Badge></div>
        <div><Icon name="users" className="file-ico" /><div className="grow"><h3>{t("settings.ad")}</h3></div><Badge kind={config.adEnabled ? "ok" : ""}>{config.adEnabled ? t("settings.enabled") : t("settings.disabled")}</Badge></div>
        <div><Icon name="repo" className="file-ico" /><div className="grow"><h3>lore://</h3><span className="muted small mono">{config.nativeRepositoryUrl}</span></div></div>
      </div>
    </div>
  </div>;
}
