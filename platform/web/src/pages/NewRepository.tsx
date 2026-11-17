import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { api } from "../api";
import { useApp } from "../App";
import { useLocale } from "../i18n";
import { Dialog, ErrorNote, Switch, useToast } from "../ui";

// crypto.randomUUID exists only in secure contexts (HTTPS, localhost); the internal-network
// HTTP mode lacks it. getRandomValues works in any context, so build a v4 UUID from it.
export function newUUID(): string {
  if (typeof crypto.randomUUID === "function") return crypto.randomUUID();
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  bytes[6] = (bytes[6] & 0x0f) | 0x40;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const hex = Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

export function NewRepositoryDialog({ onClose }: { onClose: () => void }) {
  const { t, error } = useLocale();
  const { refreshRepositories, config } = useApp();
  const toast = useToast();
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [isPublic, setPublic] = useState(false);
  const [saving, setSaving] = useState(false);
  const [problem, setProblem] = useState("");
  // One ID per dialog: a retry after a timeout creates the same repository, not a second one.
  const [id] = useState(newUUID);
  const host = (config.nativeRepositoryUrl || `lore://${window.location.hostname}`).replace(/:41337$/, "");
  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    setSaving(true);
    try {
      const repository = await api.createRepository({ id, name: name.trim(), description: description.trim(), public: isPublic });
      toast(t("newRepo.created"));
      refreshRepositories();
      onClose();
      navigate(`/repositories/${repository.id}`);
    } catch (cause) {
      setProblem(error(cause));
    } finally {
      setSaving(false);
    }
  };
  return <form onSubmit={(event) => void submit(event)}>
    <Dialog title={t("newRepo.title")} onClose={onClose} footer={<>
      <button type="button" className="btn" onClick={onClose}>{t("common.cancel")}</button>
      <button className="btn primary" disabled={saving || !name.trim()}>{t("newRepo.create")}</button>
    </>}>
      <ErrorNote message={problem} />
      <label className="field">{t("repoSettings.name")}
        <input className="input mono" required value={name} onChange={(event) => setName(event.target.value)} placeholder="team/project" pattern="[A-Za-z0-9_\-][A-Za-z0-9._\-]*(/[A-Za-z0-9_\-][A-Za-z0-9._\-]*)*" />
        <span className="hint">{host}/{name || "team/project"}</span>
      </label>
      <label className="field">{t("repoSettings.description")}<input className="input" value={description} onChange={(event) => setDescription(event.target.value)} /></label>
      <div className="setting-row boxed"><div className="grow"><h3>{t("newRepo.public")}</h3><p>{t("newRepo.publicText")}</p></div><Switch checked={isPublic} onChange={setPublic} label={t("newRepo.public")} /></div>
    </Dialog>
  </form>;
}
