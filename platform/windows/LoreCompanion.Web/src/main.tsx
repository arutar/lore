import { createRoot } from "react-dom/client";
import App from "./App";
import "./styles.css";

// A script error would otherwise leave a blank dialog with no trace: report it to the host, which
// writes it to companion.log.
const report = (text: string) => window.chrome?.webview?.postMessage({ type: "log", text } as never);
window.addEventListener("error", (event) => report(`error: ${event.message} at ${event.filename}:${event.lineno}:${event.colno}`));
window.addEventListener("unhandledrejection", (event) => report(`rejection: ${String(event.reason)}`));
createRoot(document.getElementById("root")!).render(<App />);
