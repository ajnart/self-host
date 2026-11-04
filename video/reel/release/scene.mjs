import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { loadFont } from "../readme/glyphs.mjs";

const reel = join(dirname(fileURLToPath(import.meta.url)), "..");
const fonts = join(reel, "node_modules/@fontsource-variable");
const face = (name, wght) => ({ key: `${name}${wght}`.replace(/\W/g, ""), f: loadFont(join(fonts, `${name}/files/${name}-latin-wght-normal.woff2`), { wght }) });
const heading = face("inter-tight", 800);
const mono7 = face("jetbrains-mono", 700);
const mono6 = face("jetbrains-mono", 600);

const C = { bg: "#09080f", panel: "#14121e", edge: "#3a3358", accent: "#7a57d3", at: "#a88df2", ok: "#00d787", muted: "#8a8a8a", bright: "#f5f5f5" };
const n = (v) => Math.round(v * 100) / 100;
const xml = (value) => value.replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;");

const markSrc = readFileSync(join(reel, "../../docs/assets/brand/mark/mark-dark.svg"), "utf8");
const markRects = markSrc.match(/<rect [^>]*\/>/g).join("");
const markLights = markSrc.match(/<circle [^>]*\/>/g).map((c) => ({ cx: +c.match(/cx="([\d.]+)"/)[1], cy: +c.match(/cy="([\d.]+)"/)[1] }));

export const MAX_APPS = 24;
const ROWS = 6;
const W = 880;
const PITCH = 78;
const NODE_X = 560;
const GRID = { col: 150, tile: 52, pitch: 90, label: 136, hop: 0.35 };

function kit(w, h, animate, LOOP) {
  const glyphs = new Map();
  const images = new Map();
  const sheet = [];
  const names = new Map();
  const pct = (t) => `${Math.round((t / LOOP) * 1e5) / 1e3}%`;

  const kf = (stops, { base = "" } = {}) => {
    if (!animate) return "";
    const body = stops.map(([t, css]) => `${pct(Math.max(0, Math.min(LOOP, t)))}{${css}}`).join("");
    const key = body + base;
    if (!names.has(key)) {
      const name = `k${names.size}`;
      names.set(key, name);
      sheet.push(`@keyframes ${name}{${body}}.${name}{transform-box:fill-box;transform-origin:50% 50%;${base}animation:${name} ${LOOP}s linear infinite}`);
    }
    return names.get(key);
  };
  const g = (cls, body) => (cls ? `<g class="${cls}">${body}</g>` : body);

  const measure = (fc, s, size, ls = 0) => [...s].reduce((a, ch) => a + (fc.f.glyph(ch).advance / fc.f.upm + ls) * size, 0);
  const text = (fc, s, x, y, size, fill, { ls = 0, anchor = "start", maxW } = {}) => {
    if (maxW) size = Math.min(size, (size * maxW) / measure(fc, s, size, ls));
    const k = size / fc.f.upm;
    let cx = 0;
    let uses = "";
    for (const ch of s) {
      const gl = fc.f.glyph(ch);
      if (ch !== " ") {
        const id = `${fc.key}-${ch.codePointAt(0).toString(36)}`;
        glyphs.set(id, gl.d);
        uses += `<use href="#${id}" x="${n(cx)}"/>`;
      }
      cx += gl.advance + ls * fc.f.upm;
    }
    const dx = anchor === "middle" ? -(cx * k) / 2 : anchor === "end" ? -cx * k : 0;
    return `<g transform="translate(${n(x + dx)} ${n(y)}) scale(${Math.round(k * 1e5) / 1e5})" fill="${fill}">${uses}</g>`;
  };
  const icon = (app, x, y, s) => {
    images.set(app.id, app.icon);
    return `<use href="#i-${app.id}" transform="translate(${n(x)} ${n(y)}) scale(${n(s)})"/>`;
  };
  const mark = (x, y, size) =>
    `<svg x="${n(x)}" y="${n(y)}" width="${n(size)}" height="${n(size)}" viewBox="-16 -20 256 256"><use href="#mark"/>${markLights.map((l) => `<circle cx="${l.cx}" cy="${l.cy}" r="4" fill="${C.ok}"/>`).join("")}</svg>`;
  const ring = (cx, cy, r, color, t0) =>
    animate
      ? `<circle class="${kf([[0, "transform:scale(1);stroke-width:0"], [t0 - 0.001, "transform:scale(1);stroke-width:0"], [t0, "transform:scale(1);stroke-width:2.5;animation-timing-function:cubic-bezier(.2,.8,.3,1)"], [t0 + 0.5, "transform:scale(3);stroke-width:0"], [LOOP, "transform:scale(3);stroke-width:0"]], { base: "stroke-width:0;" })}" cx="${n(cx)}" cy="${n(cy)}" r="${r}" fill="none" stroke="${color}"/>`
      : "";
  const reveal = (body, { out, inn, from }) => {
    if (!animate) return body;
    const [o0, o1] = out;
    const [i0, i1] = inn;
    const move = kf([[0, "transform:translate(0px,0px)"], [o0, "transform:translate(0px,0px);animation-timing-function:ease-in"], [o1, `transform:${from}`], [i0 - 0.001, `transform:${from}`], [i0, `transform:${from};animation-timing-function:cubic-bezier(.2,.8,.3,1)`], [i1, "transform:translate(0px,0px)"], [LOOP, "transform:translate(0px,0px)"]]);
    const stops = [[0, "visibility:visible"], [o1, "visibility:hidden"], [i0, "visibility:visible"], [LOOP, "visibility:visible"]];
    const body2 = stops.map(([t, css]) => `${pct(t)}{${css}}`).join("");
    const key = `vis${body2}`;
    if (!names.has(key)) {
      const name = `k${names.size}`;
      names.set(key, name);
      sheet.push(`@keyframes ${name}{${body2}}.${name}{animation:${name} ${LOOP}s step-end infinite}`);
    }
    return g(names.get(key), g(move, body));
  };
  const finish = ({ title, desc, body }) => {
    const defs = [...glyphs].map(([id, d]) => `<path id="${id}" d="${d}"/>`).join("")
      + [...images].map(([id, src]) => `<image id="i-${id}" width="1" height="1" preserveAspectRatio="xMidYMid meet" href="${src}"/>`).join("")
      + `<g id="mark">${markRects}</g>`
      + `<radialGradient id="bg" cx="0" cy="0" r="1" gradientUnits="userSpaceOnUse" gradientTransform="translate(200 ${n(h / 2)}) scale(800 500)"><stop offset="0" stop-color="#241651"/><stop offset=".7" stop-color="${C.bg}"/></radialGradient>`;
    const css = animate && sheet.length ? `<style>${sheet.join("")}@media (prefers-reduced-motion:reduce){*{animation:none!important}}</style>` : "";
    return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${w} ${h}" width="${w}" height="${h}" role="img" aria-labelledby="t d"><title id="t">${xml(title)}</title><desc id="d">${xml(desc)}</desc><defs>${defs}</defs>${css}<rect width="${w}" height="${h}" rx="20" fill="url(#bg)"/>${body}<rect x=".5" y=".5" width="${w - 1}" height="${h - 1}" rx="19.5" fill="none" stroke="rgba(255,255,255,.14)"/></svg>`;
  };
  return { kf, text, measure, icon, mark, ring, reveal, finish };
}

const plural = (count, one, many) => `${count} ${count === 1 ? one : many}`;
const list = (apps) => apps.map((a) => a.name).join(", ");

export function scene({ apps, version, animate }) {
  const N = apps.length;
  const cols = Math.ceil(N / ROWS);
  const rows = Math.ceil(N / cols);
  const grid = cols > 1;
  const pitch = grid ? GRID.pitch : PITCH;
  const loop = grid ? 8 : 6;
  const spread = (cols - 1) * GRID.col + GRID.tile + (GRID.label - GRID.tile) / 2 + 40;
  const w = grid ? Math.max(W, 410 + spread) : W;
  const gx = w - spread;
  const H = Math.max(350, 70 + rows * pitch);
  const { text, icon, mark, ring, reveal, kf, finish } = kit(w, H, animate, loop);
  let body = "";

  const size = 150;
  const mx = 40;
  const my = H / 2 - size / 2 - 52;
  body += mark(mx, my, size);
  const hubX = mx + ((210 + 16) / 256) * size;
  const hubY = my + ((96 + 20) / 256) * size;
  body += text(mono7, "self-host", 44, my + size + 34, 18, C.muted, { ls: -0.03 });
  body += text(heading, version, 40, my + size + 94, 62, C.bright, { ls: -0.05 });
  body += text(mono6, `+${N} ${N === 1 ? "App" : "Apps"} in the Catalog`, 42, my + size + 124, 15, C.at);

  const beam = (d, length, t0, t1) => {
    if (!animate) return "";
    const cls = kf([[0, "stroke-dashoffset:28"], [t0, "stroke-dashoffset:28;animation-timing-function:ease-in-out"], [t1, `stroke-dashoffset:-${length}`], [loop, `stroke-dashoffset:-${length}`]], { base: "stroke-dashoffset:28;" });
    return `<path class="${cls}" d="${d}" fill="none" stroke="${C.at}" stroke-width="3.5" stroke-linecap="round" stroke-dasharray="28 ${length + 28}"/>`;
  };
  const dot = (x, y, t) => `${animate ? ring(x, y, 4.5, C.ok, t) : ""}<circle cx="${n(x)}" cy="${n(y)}" r="4.5" fill="${C.ok}"/>`;

  const y0 = (H - (rows - 1) * pitch) / 2 - (grid ? 10 : 0);
  const sx = hubX + 14;
  const sy = hubY;
  for (let r = 0; r < rows; r++) {
    const y = y0 + r * pitch;
    const row = apps.slice(r * cols, r * cols + cols);
    const left = grid ? gx : NODE_X;
    const ex = left - 14;
    const d = `M${n(sx)} ${n(sy)}C${n(sx + 150)} ${n(sy)} ${n(ex - 150)} ${n(y)} ${n(ex)} ${n(y)}`;
    body += `<path d="${d}" fill="none" stroke="${C.edge}" stroke-width="2" stroke-linecap="round"/>`;
    const t0 = 0.4 + r * (grid ? 0.45 : 0.5);
    const arrive = t0 + 1.2;
    if (animate) {
      const cls = kf([[0, "stroke-dashoffset:6"], [t0, "stroke-dashoffset:6;animation-timing-function:ease-in-out"], [arrive, "stroke-dashoffset:-100"], [loop, "stroke-dashoffset:-100"]], { base: "stroke-dashoffset:6;" });
      body += `<path class="${cls}" d="${d}" pathLength="100" fill="none" stroke="${C.at}" stroke-width="3.5" stroke-linecap="round" stroke-dasharray="7 100"/>`;
    }
    body += dot(ex, y, arrive);
    let nodes = "";
    if (grid) {
      const span = (row.length - 1) * GRID.col;
      if (span > 0) {
        const track = `M${n(ex)} ${n(y)}H${n(ex + span)}`;
        body += `<path d="${track}" fill="none" stroke="${C.edge}" stroke-width="2" stroke-linecap="round"/>`;
        body += beam(track, span, arrive, arrive + GRID.hop * (row.length - 1));
        for (let c = 1; c < row.length; c++) body += dot(ex + c * GRID.col, y, arrive + GRID.hop * c);
      }
      row.forEach((a, c) => {
        const x = left + c * GRID.col;
        nodes += `<rect x="${n(x)}" y="${n(y - GRID.tile / 2)}" width="${GRID.tile}" height="${GRID.tile}" rx="14" fill="${C.panel}" stroke="${C.edge}"/>`;
        nodes += icon(a, x + 8, y - GRID.tile / 2 + 8, GRID.tile - 16);
        nodes += text(heading, a.name, x + GRID.tile / 2, y + GRID.tile / 2 + 21, 15, C.bright, { ls: -0.02, anchor: "middle", maxW: GRID.label });
      });
    } else {
      const a = row[0];
      nodes += `<rect x="${NODE_X}" y="${n(y - 30)}" width="60" height="60" rx="16" fill="${C.panel}" stroke="${C.edge}"/>`;
      nodes += icon(a, NODE_X + 9, y - 21, 42);
      nodes += text(heading, a.name, NODE_X + 78, y + 9, 25, C.bright, { ls: -0.025, maxW: W - NODE_X - 78 - 40 });
    }
    const out = loop - 1.3 + r * 0.04;
    const inn = loop - 0.85 + r * 0.1;
    body += reveal(nodes, { out: [out, out + 0.25], inn: [inn, inn + 0.35], from: "translate(40px,0px)" });
  }
  body += ring(hubX, hubY, 4, C.ok, 0.4);

  return {
    w,
    h: H,
    alt: `self-host ${version} adds ${plural(N, "App", "Apps")}: ${list(apps)}, each connected to the self-host Logo.`,
    svg: finish({ title: `self-host ${version}: ${plural(N, "new App", "new Apps")}`, desc: `The Logo sends a pulse to ${list(apps)}.`, body }),
  };
}
