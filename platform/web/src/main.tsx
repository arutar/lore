import React from "react";
import ReactDOM from "react-dom/client";
import { BrowserRouter, HashRouter } from "react-router-dom";
import { isDemo } from "./api";
import App from "./App";
import "./styles.css";

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    {/* GitHub Pages serves only files: the demo keeps its routes after the #. */}
    {isDemo ? <HashRouter><App /></HashRouter> : <BrowserRouter><App /></BrowserRouter>}
  </React.StrictMode>,
);
