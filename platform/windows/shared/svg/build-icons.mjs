// Generates the Lore Windows icon set from the master Lore mark (lore-glyph.svg).
//
// Design rules (icons-requirements.md):
//   - main sign = Lore mark on #58A6FF, mark drawn in #0D1117
//   - an operation adds a coloured badge in the lower right corner, the sign is never replaced
//   - a state is never carried by colour alone: every badge has its own shape
//   - state overlays sit in the lower left corner and keep the file icon visible
//   - the badge sits in the corner of the sign where the mark has no stroke, so it never cuts the mark
//
// Run:  node build-icons.mjs

import fs from 'node:fs';
import path from 'node:path';

const dir = process.cwd();
const LORE_D = fs.readFileSync(path.join(dir, 'lore-glyph.svg'), 'utf8').match(/<path d="([^"]+)"/)[1];

const C = {
  blue: '#58A6FF',      // main sign background
  ink: '#0D1117',       // main sign mark, terminal badge
  green: '#2EA043',     // clone, sync, commit, success, update, unlock
  deepBlue: '#1F6FEB',  // stage, status, info, staged overlay
  purple: '#8250DF',    // history
  amber: '#D29922',     // locked overlay
  orange: '#DB6D28',    // modified overlay
  red: '#CF222E',       // error, conflicted overlay, uninstaller
  grey: '#6E7781',      // ignored overlay, settings gear
  muted: '#8B949E',     // lock, empty folder
  folderBack: '#FFA000',
  folderFront: '#FFCA28',
  warnRed: '#CF1F25',
  warnYellow: '#FEC901',
  white: '#FFFFFF',
};

const f = (n) => String(Math.round(n * 10) / 10);
const pt = (r, deg) => { const a = deg * Math.PI / 180; return [r * Math.cos(a), r * Math.sin(a)]; };

function doc(title, desc, body) {
  return `<svg xmlns="http://www.w3.org/2000/svg" width="512" height="512" viewBox="0 0 512 512">
  <title>${title}</title>
  <desc>${desc}</desc>
  ${body}
</svg>
`;
}

// ---- main signs -------------------------------------------------------------
// The mark occupies x 29.92..482.08, y 0..512 inside its own 512 box, centre 256 256.
function mainSign({ box = 368, x = 12, y = 12, radius = 84, scale = 0.62, shiftX = 0, shiftY = 0 } = {}) {
  const cx = x + box / 2 + shiftX, cy = y + box / 2 + shiftY;
  const tx = cx - 256 * scale, ty = cy - 256 * scale;
  return `<rect x="${x}" y="${y}" width="${box}" height="${box}" rx="${radius}" fill="${C.blue}"/>
  <g transform="translate(${f(tx)} ${f(ty)}) scale(${scale})"><path d="${LORE_D}" fill="${C.ink}"/></g>`;
}

const SIGN = mainSign({ box: 368, x: 12, y: 12 });        // 368 px, corner free for the badge
const SIGN_PLAIN = mainSign({ box: 368, x: 72, y: 72 });  // 368 px, centred, no badge
const SIGN_APP = mainSign({ box: 464, x: 24, y: 24, radius: 100, scale: 0.68 });

// ---- operation badge, lower right corner ------------------------------------
function badge(color, symbol, { cx = 396, cy = 396, r = 104, ring = 20, scale = 0.86, stroke = 30 } = {}) {
  return `<circle cx="${cx}" cy="${cy}" r="${r}" fill="${color}" stroke="${C.white}" stroke-width="${ring}"/>
  <g transform="translate(${cx} ${cy}) scale(${scale})" fill="none" stroke="${C.white}" stroke-width="${stroke}" stroke-linecap="round" stroke-linejoin="round">${symbol}</g>`;
}

// ---- overlay badge, lower left corner ---------------------------------------
function overlay(color, symbol, { cx = 150, cy = 362, r = 132, ring = 24, scale = 1.15, stroke = 30 } = {}) {
  return `<circle cx="${cx}" cy="${cy}" r="${r}" fill="${color}" stroke="${C.white}" stroke-width="${ring}"/>
  <g transform="translate(${cx} ${cy}) scale(${scale})" fill="none" stroke="${C.white}" stroke-width="${stroke}" stroke-linecap="round" stroke-linejoin="round">${symbol}</g>`;
}

// ---- symbols, drawn around the origin ---------------------------------------
const arrowDown = `<path d="M0 -62 V34"/><path d="M-38 6 L0 44 L38 6"/>`;
const sym = {
  arrowDown,
  clone: `${arrowDown}<path d="M-52 66 H52"/>`,
  installer: `<path d="M0 -62 V14"/><path d="M-32 -12 L0 20 L32 -12"/><path d="M-52 34 V62 H52 V34"/>`,
  minus: `<path d="M-54 0 H54"/>`,
  plus: `<path d="M0 -56 V56"/><path d="M-56 0 H56"/>`,
  check: `<path d="M-52 2 L-14 42 L56 -44"/>`,
  info: `<path d="M0 -12 V52" stroke-width="32"/><circle cx="0" cy="-52" r="19" fill="${C.white}" stroke="none"/>`,
  bang: `<path d="M0 -58 V20" stroke-width="34"/><circle cx="0" cy="58" r="20" fill="${C.white}" stroke="none"/>`,
  cross: `<path d="M-42 -42 L42 42"/><path d="M42 -42 L-42 42"/>`,
  lock: (open) => `<rect x="-46" y="-6" width="92" height="70" rx="14" fill="${C.white}" stroke="none"/>
  <path d="M-26 -6 V-40 A${open ? '28 28' : '26 26'} 0 0 1 ${open ? 28 : 26} ${open ? -42 : -40} V${open ? -22 : -6}" stroke-width="20"/>`,
  terminal: `<path d="M-46 -34 L-10 0 L-46 34" stroke-width="24"/><path d="M6 34 H50" stroke-width="24"/>`,
  pencil: (color) => `<g transform="rotate(-45)" fill="${C.white}" stroke="none">
    <path d="M-78 0 L-40 -22 L-40 22 Z"/>
    <rect x="-40" y="-22" width="84" height="44"/>
    <rect x="52" y="-18" width="26" height="36" rx="10"/>
    <path d="M48 -22 V22" stroke="${color}" stroke-width="9"/>
  </g>`,
};

const commitSymbol = `<g transform="translate(-2 1)">${sym.check}</g>`;
const successSymbol = `<g transform="translate(-2 6.8829176)">${sym.check}</g>`;
const historySymbol = `<circle cx="0" cy="0" r="41.28"/><path d="M-2.96085 5.6939439 V-21.8260561"/><path d="M-2.96085 5.6939439 H17.67915"/>`;

// Circular arrow, clockwise on screen from a0 to a1, arrow head at a1.
function arcArrow(r, a0, a1, headLen = 26, headHalf = 18, sweep = true, stroke = 29) {
  const [x0, y0] = pt(r, a0);
  const [x1, y1] = pt(r, a1);
  const delta = sweep ? a1 - a0 : a0 - a1;
  const large = Math.abs(delta) > 180 ? 1 : 0;
  const arc = `<path d="M${f(x0)} ${f(y0)} A${r} ${r} 0 ${large} ${sweep ? 1 : 0} ${f(x1)} ${f(y1)}"/>`;
  const s = sweep ? 1 : -1;
  const t = [-s * Math.sin(a1 * Math.PI / 180), s * Math.cos(a1 * Math.PI / 180)];
  const n = [t[1], -t[0]];
  const half = headHalf + stroke / 2;
  const tip = [x1 + t[0] * (headLen + stroke / 2), y1 + t[1] * (headLen + stroke / 2)];
  const b1 = [x1 + n[0] * half, y1 + n[1] * half];
  const b2 = [x1 - n[0] * half, y1 - n[1] * half];
  return `${arc}<path d="M${f(tip[0])} ${f(tip[1])} L${f(b1[0])} ${f(b1[1])} L${f(b2[0])} ${f(b2[1])} Z" fill="${C.white}" stroke="none"/>`;
}

const syncSymbol = `<path d="M-36.65904 -25.67077 a44.72 44.72 0 0 1 81.24209 21.76875"/>
  <path d="M53.922 17.458 L68.284 -24.854 L15.738 -5.766 Z" fill="${C.white}" stroke="none"/>
  <path d="M36.65904 25.67077 A44.72 44.72 0 0 1 -44.58305 3.90202"/>
  <path d="M-53.922 -17.458 L-68.284 24.854 L-15.738 5.766 Z" fill="${C.white}" stroke="none"/>`;
const updateSymbol = `${arcArrow(58, 110, 380, 30, 20)}`;

// Gear silhouette with a transparent hole (even-odd fill).
function gear(rOut, rRoot, hole, teeth = 8, half = 13) {
  const step = 360 / teeth;
  const p = [];
  for (let i = 0; i < teeth; i++) {
    const c = i * step;
    p.push(pt(rRoot, c - step / 2 + 4), pt(rOut, c - half), pt(rOut, c + half), pt(rRoot, c + step / 2 - 4));
  }
  const ringPath = p.map((q, i) => `${i ? 'L' : 'M'}${f(q[0])} ${f(q[1])}`).join(' ');
  const eye = `M${f(hole)} 0 A${hole} ${hole} 0 1 1 ${f(-hole)} 0 A${hole} ${hole} 0 1 1 ${f(hole)} 0 Z`;
  return `<path d="${ringPath} Z ${eye}" fill="${C.grey}" fill-rule="evenodd"/>`;
}

// Folder silhouette with a raised tab and a front plate, drawn in a 48 x 48 box.
const FOLDER_BACK = `M40,12H22l-4-4H8c-2.2,0-4,1.8-4,4v8h40v-4C44,13.8,42.2,12,40,12z`;
const FOLDER_FRONT = `M40,12H8c-2.2,0-4,1.8-4,4v20c0,2.2,1.8,4,4,4h32c2.2,0,4-1.8,4-4V16C44,13.8,42.2,12,40,12z`;

function folder({ back = C.folderBack, front = C.folderFront } = {}) {
  return `<g transform="scale(10.6667)">
    <path d="${FOLDER_BACK}" fill="${back}"/>
    <path d="${FOLDER_FRONT}" fill="${front}"/>
  </g>`;
}

function stateIcon(color, symbol, { stroke = 44, scale = 1 } = {}) {
  return `<circle cx="256" cy="256" r="216" fill="${color}"/>
  <g transform="translate(256 256) scale(${scale})" fill="none" stroke="${C.white}" stroke-width="${stroke}" stroke-linecap="round" stroke-linejoin="round">${symbol}</g>`;
}

const out = new Map();
const action = (name, title, desc, color, symbol, options = {}) =>
  out.set(name, doc(title, desc, `${SIGN}\n  ${badge(color, symbol, options)}`));

// ---- Explorer: actions and Companion windows --------------------------------
out.set('lore', doc('Lore', 'Lore main sign: the Lore mark on blue.', SIGN_PLAIN));
action('clone', 'Lore Clone', 'Clone a repository: Lore mark with a green download arrow.', C.green, sym.clone);
action('sync', 'Lore Sync', 'Sync a repository: Lore mark with green circular arrows.', C.green, syncSymbol, { scale: 1, stroke: 25.8 });
action('commit', 'Lore Commit', 'Commit changes: Lore mark with a green check.', C.green, commitSymbol);
action('stage', 'Lore Stage', 'Stage selected paths: Lore mark with a blue plus.', C.deepBlue, sym.plus);
action('status', 'Lore Status', 'Repository status: Lore mark with an information sign.', C.deepBlue, sym.info);
action('history', 'Lore History', 'Revision history: Lore mark with a clock.', C.purple, historySymbol, { scale: 1, stroke: 15.48 });
action('lock', 'Lore Acquire Locks', 'Acquire locks: Lore mark with a closed padlock.', C.muted, sym.lock(false));
action('unlock', 'Lore Release Locks', 'Release locks: Lore mark with an open padlock.', C.green, sym.lock(true));
out.set('folder-open', doc('Select folder', 'Open folder used to pick a directory.', folder()));

// ---- Explorer: state overlays, lower left corner ----------------------------
out.set('overlay-modified', doc('Lore modified', 'Orange pencil overlay for the lower left corner of a file icon.',
  overlay(C.orange, sym.pencil(C.orange))));
out.set('overlay-staged', doc('Lore staged', 'Blue plus overlay for the lower left corner of a file icon.',
  overlay(C.deepBlue, sym.plus)));
out.set('overlay-conflicted', doc('Lore conflicted', 'Red exclamation overlay for the lower left corner of a file icon.',
  overlay(C.red, sym.bang)));
out.set('overlay-locked', doc('Lore locked', 'Closed padlock overlay for the lower left corner of a file icon.',
  overlay(C.amber, sym.lock(false))));
out.set('overlay-ignored', doc('Lore ignored', 'Grey minus overlay for the lower left corner of a file icon.',
  overlay(C.grey, sym.minus)));
out.set('overlay-staged-modified', doc('Lore staged and modified',
  'Blue plus badge with an orange pencil badge for the lower left corner of a file icon.',
  `${overlay(C.orange, sym.pencil(C.orange), { cx: 256, cy: 256, r: 72, ring: 20, scale: 0.62, stroke: 22 })}
  ${overlay(C.deepBlue, sym.plus, { cx: 120, cy: 392, r: 104, ring: 24, scale: 0.9, stroke: 32 })}`));

// ---- Interface states -------------------------------------------------------
out.set('success', doc('Success', 'Green check.', stateIcon(C.green, successSymbol, { scale: 1.7 })));
out.set('warning', doc('Warning', 'Red-rimmed yellow triangle with a black exclamation.',
  `<g transform="translate(0 23.625) scale(4.1666667)">
    <path d="M2.35,84.42,45.28,10.2l.17-.27h0A23,23,0,0,1,52.5,2.69,17,17,0,0,1,61.57,0a16.7,16.7,0,0,1,9.11,2.69,22.79,22.79,0,0,1,7,7.26q.19.32.36.63l42.23,73.34.24.44h0a22.48,22.48,0,0,1,2.37,10.19,17.63,17.63,0,0,1-2.17,8.35,15.94,15.94,0,0,1-6.93,6.6c-.19.1-.39.18-.58.26a21.19,21.19,0,0,1-9.11,1.75v0H17.61c-.22,0-.44,0-.65,0a18.07,18.07,0,0,1-6.2-1.15A16.42,16.42,0,0,1,3,104.24a17.53,17.53,0,0,1-3-9.57,23,23,0,0,1,1.57-8.74,7.66,7.66,0,0,1,.77-1.51Z" fill="${C.warnRed}"/>
    <path d="M9,88.75,52.12,14.16c5.24-8.25,13.54-8.46,18.87,0l42.43,73.69c3.39,6.81,1.71,16-9.33,15.77H17.61C10.35,103.8,5.67,97.43,9,88.75Z" fill="${C.warnYellow}" fill-rule="evenodd"/>
    <path d="M57.57,83.78A5.53,5.53,0,0,1,61,82.2a5.6,5.6,0,0,1,2.4.36,5.7,5.7,0,0,1,2,1.3,5.56,5.56,0,0,1,1.54,5,6.23,6.23,0,0,1-.42,1.35,5.57,5.57,0,0,1-5.22,3.26,5.72,5.72,0,0,1-2.27-.53A5.51,5.51,0,0,1,56.28,90a5.18,5.18,0,0,1-.36-1.27,5.83,5.83,0,0,1-.06-1.31h0a6.53,6.53,0,0,1,.57-2,4.7,4.7,0,0,1,1.14-1.56Zm8.15-10.24c-.19,4.79-8.31,4.8-8.49,0-.82-8.21-2.92-29.34-2.86-37.05.07-2.38,2-3.79,4.56-4.33a12.83,12.83,0,0,1,5,0c2.61.56,4.65,2,4.65,4.44v.24L65.72,73.54Z" fill="#010101"/>
  </g>`));
out.set('error', doc('Error', 'Red cross.', stateIcon(C.red, sym.cross, { scale: 1.6 })));
out.set('info', doc('Information', 'Blue information sign.', stateIcon(C.deepBlue, sym.info, { stroke: 40, scale: 1.6 })));
out.set('progress', doc('Running operation', 'Spinner, used only where a spinner is shown.',
  `<circle cx="256" cy="256" r="176" fill="none" stroke="${C.blue}" stroke-opacity="0.22" stroke-width="48"/>
  <path d="M256 80 A176 176 0 1 1 80 256" fill="none" stroke="${C.blue}" stroke-width="48" stroke-linecap="round"/>`));
out.set('empty', doc('Empty repository or catalog', 'Neutral empty folder.',
  folder({ back: '#57606A', front: '#8B949E' })));
out.set('settings', doc('Settings', 'Gear.', `<g transform="translate(256 256)">${gear(212, 158, 78)}</g>`));

// ---- Application and install ------------------------------------------------
out.set('app', doc('Lore', 'Lore application icon: the Lore mark on blue.', SIGN_APP));
action('app-installer', 'Lore installer', 'Install Lore: Lore mark with a green install badge.', C.green, sym.installer);
action('app-uninstaller', 'Lore uninstaller', 'Remove Lore: Lore mark with a red removal badge.', C.red, sym.minus);
action('cli', 'Lore CLI', 'Lore command line: Lore mark with a terminal badge.', C.ink, sym.terminal);
action('update', 'Lore update', 'Update the Lore client: Lore mark with a green update arrow.', C.green, updateSymbol);

for (const [name, content] of out) fs.writeFileSync(path.join(dir, `${name}.svg`), content);
console.log(`wrote ${out.size} icons`);
