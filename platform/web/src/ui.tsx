import { createContext, useCallback, useContext, useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { useLocale } from "./i18n";

const paths = {
  repo: <><path d="M4 6.5A2.5 2.5 0 0 1 6.5 4H20v13H6.5A2.5 2.5 0 0 0 4 19.5z" /><path d="M4 19.5A2.5 2.5 0 0 0 6.5 22H20v-5" /></>,
  users: <><circle cx="9" cy="8" r="3.5" /><path d="M2.5 20a6.5 6.5 0 0 1 13 0M16 4.6a3.5 3.5 0 0 1 0 6.8M18 14.5a6.5 6.5 0 0 1 3.5 5.5" /></>,
  user: <><circle cx="12" cy="8" r="4" /><path d="M4 21a8 8 0 0 1 16 0" /></>,
  audit: <><path d="M8 4h11a1 1 0 0 1 1 1v15l-3-2-3 2-3-2-3 2V5a1 1 0 0 1 1-1z" /><path d="M11 9h6M11 13h6" /><path d="M4 8v9" /></>,
  settings: <><circle cx="12" cy="12" r="3" /><path d="M19 12a7 7 0 0 0-.1-1.3l2-1.5-2-3.4-2.3.9a7 7 0 0 0-2.2-1.3L14 3h-4l-.4 2.4a7 7 0 0 0-2.2 1.3l-2.3-.9-2 3.4 2 1.5a7 7 0 0 0 0 2.6l-2 1.5 2 3.4 2.3-.9a7 7 0 0 0 2.2 1.3L10 21h4l.4-2.4a7 7 0 0 0 2.2-1.3l2.3.9 2-3.4-2-1.5c.1-.4.1-.9.1-1.3z" /></>,
  search: <><circle cx="11" cy="11" r="7" /><path d="m20 20-3.6-3.6" /></>,
  plus: <path d="M12 5v14M5 12h14" />,
  check: <path d="m5 12.5 4.5 4.5L19 7" />,
  x: <path d="M6 6l12 12M18 6 6 18" />,
  copy: <><rect x="9" y="9" width="11" height="11" rx="2" /><path d="M15 9V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v7a2 2 0 0 0 2 2h3" /></>,
  globe: <><circle cx="12" cy="12" r="9" /><path d="M3 12h18M12 3a14 14 0 0 1 0 18M12 3a14 14 0 0 0 0 18" /></>,
  lock: <><rect x="5" y="10.5" width="14" height="10" rx="2" /><path d="M8 10.5V7.5a4 4 0 0 1 8 0v3" /></>,
  unlock: <><rect x="5" y="10.5" width="14" height="10" rx="2" /><path d="M8 10.5V7.5a4 4 0 0 1 7.7-1.5" /></>,
  folder: <path d="M3 7a2 2 0 0 1 2-2h4.5l2 2.5H19a2 2 0 0 1 2 2V18a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" />,
  file: <><path d="M7 3h7l5 5v12a1 1 0 0 1-1 1H7a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1z" /><path d="M14 3v5h5" /></>,
  download: <path d="M12 4v11m0 0-4-4m4 4 4-4M5 20h14" />,
  upload: <path d="M12 16V5m0 0-4 4m4-4 4 4M5 20h14" />,
  history: <><path d="M3.5 12a8.5 8.5 0 1 0 2.6-6.1L3.5 8.5" /><path d="M3.5 4v4.5H8M12 7.5V12l3 2" /></>,
  shield: <path d="M12 3 5 6v5.5c0 4.3 2.9 7.7 7 9.5 4.1-1.8 7-5.2 7-9.5V6z" />,
  clock: <><circle cx="12" cy="12" r="9" /><path d="M12 7v5l3 2" /></>,
  info: <><circle cx="12" cy="12" r="9" /><path d="M12 11v6M12 7.5v.01" /></>,
  out: <path d="M15 4h3a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2h-3M10 16l4-4-4-4M14 12H4" />,
  chevron: <path d="m9 6 6 6-6 6" />,
  warn: <><path d="M12 4 2.5 20h19z" /><path d="M12 10v5M12 17.5v.01" /></>,
  terminal: <><rect x="3" y="4" width="18" height="16" rx="2" /><path d="m7 9 3 3-3 3M13 15h4" /></>,
};

export type IconName = keyof typeof paths;

export function Icon({ name, className = "" }: { name: IconName; className?: string }) {
  return <svg className={`i ${className}`} viewBox="0 0 24 24" aria-hidden="true">{paths[name]}</svg>;
}

const tones = ["var(--accent)", "var(--accent-2)", "var(--ok)", "var(--warn)"];
export const tone = (text: string) => tones[[...text].reduce((hash, character) => (hash * 31 + character.charCodeAt(0)) >>> 0, 7) % tones.length];
const initials = (text: string) => text.split(/[\s._@-]+/).filter(Boolean).slice(0, 2).map((word) => word[0]!.toUpperCase()).join("") || "?";

export function Avatar({ name, square, small }: { name: string; square?: boolean; small?: boolean }) {
  return <span className={`avatar ${square ? "sq" : ""} ${small ? "sm" : ""}`} style={{ "--tone": tone(name) } as React.CSSProperties}>{initials(name)}</span>;
}

export function RepoTile({ name, large }: { name: string; large?: boolean }) {
  return <span className={`repo-tile ${large ? "lg" : ""}`} style={{ "--tone": tone(name) } as React.CSSProperties}><Icon name="repo" /></span>;
}

export function Badge({ kind = "", icon, children, title }: { kind?: "" | "ok" | "accent" | "warn" | "danger"; icon?: IconName; children: ReactNode; title?: string }) {
  return <span className={`badge ${kind}`} title={title}>{icon && <Icon name={icon} />}{children}</span>;
}

export function Switch({ checked, onChange, label, disabled }: { checked: boolean; onChange: (checked: boolean) => void; label: string; disabled?: boolean }) {
  return <label className="switch" title={label}><input type="checkbox" checked={checked} disabled={disabled} aria-label={label} onChange={(event) => onChange(event.target.checked)} /><i /></label>;
}

export function Empty({ icon, title, text, children }: { icon: IconName; title: string; text?: string; children?: ReactNode }) {
  return <div className="empty"><div className="ico"><Icon name={icon} /></div><h2>{title}</h2>{text && <p>{text}</p>}{children}</div>;
}

export function Loading() {
  const { t } = useLocale();
  return <div className="empty"><p>{t("app.loading")}</p></div>;
}

export function ErrorNote({ message, onRetry }: { message: string; onRetry?: () => void }) {
  const { t } = useLocale();
  if (!message) return null;
  return <div className="note warn" role="alert"><Icon name="warn" /><span className="grow">{message}</span>{onRetry && <button className="btn sm" onClick={onRetry}>{t("app.retry")}</button>}</div>;
}

/* ── toasts ── */

interface Toast { id: number; text: string; error?: boolean; undo?: () => void }
const ToastContext = createContext<(text: string, options?: { error?: boolean; undo?: () => void }) => void>(() => {});
export const useToast = () => useContext(ToastContext);

export function ToastProvider({ children }: { children: ReactNode }) {
  const { t } = useLocale();
  const [toasts, setToasts] = useState<Toast[]>([]);
  const next = useRef(1);
  const show = useCallback((text: string, options: { error?: boolean; undo?: () => void } = {}) => {
    const toast = { id: next.current++, text, ...options };
    setToasts((items) => [...items, toast]);
    window.setTimeout(() => setToasts((items) => items.filter((item) => item.id !== toast.id)), toast.undo ? 7000 : 4500);
  }, []);
  return <ToastContext.Provider value={show}>
    {children}
    <div className="toasts" aria-live="polite">{toasts.map((toast) => <div key={toast.id} className={`toast ${toast.error ? "err" : ""}`} role="status">
      <Icon name={toast.error ? "warn" : "check"} /><span className="grow">{toast.text}</span>
      {toast.undo && <button onClick={() => { toast.undo?.(); setToasts((items) => items.filter((item) => item.id !== toast.id)); }}>{t("common.undo")}</button>}
    </div>)}</div>
  </ToastContext.Provider>;
}

/* ── dialog ── */

export function Dialog({ title, onClose, children, footer, width }: { title: string; onClose: () => void; children: ReactNode; footer?: ReactNode; width?: number }) {
  const { t } = useLocale();
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    ref.current?.querySelector<HTMLElement>("input, select, textarea, button")?.focus();
    const onKey = (event: KeyboardEvent) => { if (event.key === "Escape") onClose(); };
    window.addEventListener("keydown", onKey);
    return () => { window.removeEventListener("keydown", onKey); previous?.focus?.(); };
  }, [onClose]);
  return <div className="overlay" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
    <div className="dialog" role="dialog" aria-modal="true" aria-label={title} ref={ref} style={width ? { width: `min(${width}px, 100%)` } : undefined}>
      <header><h2 className="grow">{title}</h2><button type="button" className="btn quiet icon sm" onClick={onClose} aria-label={t("common.close")}><Icon name="x" /></button></header>
      <div className="body">{children}</div>
      {footer && <footer>{footer}</footer>}
    </div>
  </div>;
}

/* ── top bar: each page states its title, description and actions ── */

export interface TopBarState { title?: ReactNode; description?: ReactNode; actions?: ReactNode }
export const TopBarContext = createContext<(state: TopBarState) => void>(() => {});

export function useTopBar(state: TopBarState, deps: unknown[]) {
  const set = useContext(TopBarContext);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => { set(state); }, deps);
  useEffect(() => () => set({}), [set]);
}

/* ── copy to clipboard ── */

// navigator.clipboard exists only in secure contexts (HTTPS, localhost); the internal-network
// HTTP mode lacks it. There the deprecated but universally supported execCommand("copy") still
// works inside a click handler.
async function writeClipboard(text: string) {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(text);
    return;
  }
  const active = document.activeElement as HTMLElement | null;
  const area = document.createElement("textarea");
  area.value = text;
  area.setAttribute("readonly", "");
  area.style.position = "fixed";
  area.style.top = "-1000px";
  area.style.opacity = "0";
  document.body.appendChild(area);
  try {
    area.select();
    if (!document.execCommand("copy")) throw new Error("copy command rejected");
  } finally {
    area.remove();
    active?.focus();
  }
}

export function useCopy() {
  const { t } = useLocale();
  const toast = useToast();
  return useCallback(async (text: string, message: string) => {
    try {
      await writeClipboard(text);
      toast(message);
      return true;
    } catch {
      toast(t("error.clipboard"), { error: true });
      return false;
    }
  }, [t, toast]);
}

export function CopyButton({ text, label, message, small }: { text: string; label: string; message: string; small?: boolean }) {
  const copy = useCopy();
  const [done, setDone] = useState(false);
  return <button type="button" className={`btn quiet icon ${small ? "sm" : ""} ${done ? "done" : ""}`} title={label} aria-label={label}
    onClick={async (event) => { event.preventDefault(); event.stopPropagation(); if (await copy(text, message)) { setDone(true); window.setTimeout(() => setDone(false), 1500); } }}>
    <Icon name={done ? "check" : "copy"} />
  </button>;
}

export function formatSize(size: number, language: string) {
  const units = language === "ru" ? ["Б", "КБ", "МБ", "ГБ", "ТБ"] : ["B", "KB", "MB", "GB", "TB"];
  let value = size;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit++; }
  return `${unit === 0 ? value : value.toLocaleString(language, { maximumFractionDigits: 1 })} ${units[unit]}`;
}
