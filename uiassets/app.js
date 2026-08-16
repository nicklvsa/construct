"use strict";

// ---------- token & api ----------

const token = new URLSearchParams(location.search).get("t") || "";
if (token) history.replaceState(null, "", "/");

async function api(path, body) {
	const opts = { headers: { "X-Construct-Token": token } };
	if (body !== undefined) {
		opts.method = "POST";
		opts.headers["Content-Type"] = "application/json";
		opts.body = JSON.stringify(body);
	}
	const r = await fetch(path, opts);
	const j = await r.json().catch(() => ({}));
	j._status = r.status;
	return j;
}

// ---------- state ----------

let state = null;
let activeFile = null;         // path of active tab
let sel = null;                // {file, name}
let filter = "";
let view = "edit";             // edit | graph
let edTab = "command";         // command | glue | raw
let bodyTimer = null;
let bodyMode = "code";         // code | structure
let lastBodySel = null;
let selectedEdge = null;       // {from, to} in graph view
let graphClickGuard = false;
let graphResizeHandler = null;
const graphPos = new Map();    // node name -> {x, y}, session-persistent
const edgeWaypoints = new Map(); // "from\0to" -> [{x, y}, ...], session-persistent

function edgeKey(from, to) { return from + "\u0000" + to; }

// splinePath renders a smooth catmull-rom path through the given points.
function splinePath(pts) {
	if (pts.length < 2) return "";
	let d = `M ${pts[0].x} ${pts[0].y}`;
	for (let i = 0; i < pts.length - 1; i++) {
		const p0 = pts[i - 1] || pts[i];
		const p1 = pts[i];
		const p2 = pts[i + 1];
		const p3 = pts[i + 2] || p2;
		const c1x = p1.x + (p2.x - p0.x) / 6, c1y = p1.y + (p2.y - p0.y) / 6;
		const c2x = p2.x - (p3.x - p1.x) / 6, c2y = p2.y - (p3.y - p1.y) / 6;
		d += ` C ${c1x.toFixed(1)} ${c1y.toFixed(1)}, ${c2x.toFixed(1)} ${c2y.toFixed(1)}, ${p2.x.toFixed(1)} ${p2.y.toFixed(1)}`;
	}
	return d;
}

function edgePts(from, to) {
	const W = 178, H = 44;
	const a = graphPos.get(from), b = graphPos.get(to);
	if (!a || !b) return null;
	const wps = edgeWaypoints.get(edgeKey(from, to)) || [];
	return [{ x: a.x + W / 2, y: a.y + H }, ...wps, { x: b.x + W / 2, y: b.y }];
}

function edgeD(from, to) {
	const pts = edgePts(from, to);
	if (!pts) return "";
	if ((edgeWaypoints.get(edgeKey(from, to)) || []).length === 0) {
		const W = 178, H = 44;
		const a = pts[0], b = pts[pts.length - 1];
		const my = (a.y + b.y) / 2;
		return `M ${a.x} ${a.y} C ${a.x} ${my}, ${b.x} ${my}, ${b.x} ${b.y}`;
	}
	return splinePath(pts);
}

// syncEdgeVisuals refreshes one edge's paths and its waypoint dots.
function syncEdgeVisuals(svg, from, to) {
	const g = svg.querySelector(`.gedge[data-from="${from}"][data-to="${to}"]`);
	if (g) {
		const d = edgeD(from, to);
		g.querySelectorAll("path").forEach((p) => p.setAttribute("d", d));
	}
	const layer = svg.querySelector(".ehlayer");
	if (!layer) return;
	const wps = edgeWaypoints.get(edgeKey(from, to)) || [];
	layer.querySelectorAll(`.whandle[data-from="${from}"][data-to="${to}"]`).forEach((h) => h.remove());
	for (let i = 0; i < wps.length; i++) {
		const c = document.createElementNS("http://www.w3.org/2000/svg", "circle");
		c.setAttribute("class", "whandle");
		c.setAttribute("data-from", from);
		c.setAttribute("data-to", to);
		c.setAttribute("data-wp", i);
		c.setAttribute("cx", wps[i].x);
		c.setAttribute("cy", wps[i].y);
		c.setAttribute("r", 4.5);
		const t = document.createElementNS("http://www.w3.org/2000/svg", "title");
		t.textContent = "drag to reshape · double-click to remove";
		c.appendChild(t);
		layer.appendChild(c);
	}
}

// ---------- syntax highlighting ----------

const HL_BUILTINS = new Set(["cp", "rm", "mkdir", "touch", "download", "extract"]);
const HL_KEYWORDS = new Set(["if", "else", "for", "parallel", "matrix", "env", "invoke", "switch", "case", "default", "in", "lock", "onfail", "fail", "confirm", "prompt", "input", "state", "var", "global", "retry", "timeout", "continue", "break"]);
const HL_TOKEN = /("(?:[^"\\]|\\.)*")|(&[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z0-9_]+)*)|(@[A-Za-z_][A-Za-z0-9_]*(?::-[^\s,"')]+)?)|(\\[&@$])|(\$\{[^}]*\})|(\b\d+\b)|(<[^<>\n]*>)|([A-Za-z_][A-Za-z0-9_]*)/g;

function hlSpan(cls, s) { return `<span class="${cls}">${esc(s)}</span>`; }

function hlCommentStart(s) {
	let inStr = false;
	for (let i = 0; i < s.length; i++) {
		const c = s[i];
		if (c === "\\") { i++; continue; }
		if (c === '"') inStr = !inStr;
		if (!inStr && (c === "#" || (c === "/" && s[i + 1] === "/")) &&
			(i === 0 || s[i - 1] === " " || s[i - 1] === "\t")) return i;
	}
	return -1;
}

function hlCode(s, shellMode) {
	let out = "", last = 0;
	for (const m of s.matchAll(HL_TOKEN)) {
		const i = m.index;
		if (i > last) out += esc(s.slice(last, i));
		const tok = m[0];
		if (m[1] !== undefined) out += hlSpan("tk-str", tok);
		else if (m[2] !== undefined) out += hlSpan("tk-ref", tok);
		else if (m[3] !== undefined) out += hlSpan("tk-env", tok);
		else if (m[4] !== undefined) out += hlSpan("tk-dim", tok);
		else if (m[5] !== undefined) out += hlSpan("tk-dim", tok);
		else if (m[6] !== undefined) out += hlSpan("tk-num", tok);
		else if (m[7] !== undefined) out += hlSpan("tk-num", tok);
		else if (shellMode) out += esc(tok);
		else if (HL_KEYWORDS.has(tok)) out += hlSpan("tk-kw", tok);
		else out += esc(tok);
		last = i + tok.length;
	}
	if (last < s.length) out += esc(s.slice(last));
	return out;
}

function hlLine(line) {
	if (line.trim() === "") return "";
	const t = line.trimStart();
	if (t.startsWith("#") || t.startsWith("//")) return hlSpan("tk-com", line);
	const cs = hlCommentStart(line);
	const code = cs >= 0 ? line.slice(0, cs) : line;
	const com = cs >= 0 ? hlSpan("tk-com", line.slice(cs)) : "";
	const firstWord = (t.match(/^[A-Za-z_][A-Za-z0-9_-]*/) || [""])[0];
	let sigil = "";
	let rest = line;
	if (t.startsWith("$") || t.startsWith("!")) {
		const lead = line.length - t.length;
		const sig = (t[0] === "!" && t[1] === "$") ? 2 : 1;
		sigil = esc(line.slice(0, lead)) + hlSpan("tk-sh", line.slice(lead, lead + sig));
		rest = line.slice(lead + sig);
	}
	const shellMode = sigil !== "" || HL_BUILTINS.has(firstWord);
	return sigil + hlCode(rest, shellMode) + com;
}

function highlightConstfile(text) {
	return text.split("\n").map(hlLine).join("\n");
}

function codeEditorHTML(id, text, height) {
	return `<div class="codeedit" ${height ? `style="min-height:${height}"` : ""}>
		<pre class="hlpre" aria-hidden="true"><code>${highlightConstfile(text)}</code></pre>
		<textarea id="${id}" spellcheck="false" wrap="off">${esc(text)}</textarea>
	</div>`;
}

function wireCodeEditor(ta) {
	const code = ta.parentElement.querySelector("code");
	const pre = ta.parentElement.querySelector("pre");
	const paint = () => { code.innerHTML = highlightConstfile(ta.value) + "\n"; };
	const sync = () => { pre.scrollTop = ta.scrollTop; pre.scrollLeft = ta.scrollLeft; };
	ta.addEventListener("input", paint);
	ta.addEventListener("scroll", sync);
	ta.addEventListener("keydown", (e) => {
		if (e.key === "Tab") {
			e.preventDefault();
			insertAtCaret(ta, "    ");
		} else if (e.key === "Enter") {
			e.preventDefault();
			const s = ta.selectionStart;
			const lineStart = ta.value.lastIndexOf("\n", s - 1) + 1;
			const cur = ta.value.slice(lineStart, s);
			const indent = (cur.match(/^[ \t]*/) || [""])[0] + (cur.trimEnd().endsWith("{") ? "    " : "");
			insertAtCaret(ta, "\n" + indent);
		}
	});
	sync();
}

const $ = (id) => document.getElementById(id);
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({
	"&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
}[c]));

function toast(msg, kind = "", ms = 3400) {
	const el = document.createElement("div");
	el.className = "toast " + kind;
	el.textContent = msg;
	$("toasts").appendChild(el);
	setTimeout(() => el.remove(), ms);
}

function activeFileState() {
	return state?.files?.find((f) => f.path === activeFile) || null;
}

function selCommand() {
	const f = activeFileState();
	if (!f) return null;
	return f.commands.find((c) => c.name === sel?.name && sel?.file === f.path) || null;
}

// ---------- boot ----------

async function boot() {
	const r = await api("/api/state");
	if (r._status === 403) {
		document.body.innerHTML = '<div style="margin:auto;color:#f87171;font-family:monospace">forbidden — open construct ui from the terminal to get a fresh link</div>';
		return;
	}
	state = r.state;
	activeFile = state.main;
	bindGlobal();
	render();
}

function bindGlobal() {
	$("btnSave").onclick = save;
	$("btnUndo").onclick = async () => { await api("/api/undo"); refresh(); };
	$("btnRedo").onclick = async () => { await api("/api/redo"); refresh(); };
	$("viewEdit").onclick = () => { view = "edit"; render(); };
	$("viewGraph").onclick = () => { view = "graph"; render(); };
	$("search").oninput = () => { filter = $("search").value.toLowerCase(); renderCards(); };
	$("btnAdd").onclick = addCommandModal;
	$("btnAddImport").onclick = addImportModal;
	document.addEventListener("keydown", (e) => {
		const mod = e.ctrlKey || e.metaKey;
		if (mod && e.key.toLowerCase() === "s") { e.preventDefault(); save(); }
		const typing = /INPUT|TEXTAREA|SELECT/.test(document.activeElement?.tagName || "");
		if (mod && !typing && e.key.toLowerCase() === "z") { e.preventDefault(); api("/api/undo").then(refresh); }
		if (mod && !typing && e.key.toLowerCase() === "y") { e.preventDefault(); api("/api/redo").then(refresh); }
		if ((e.key === "Delete" || e.key === "Backspace") && view === "graph" && selectedEdge && !typing) {
			e.preventDefault();
			deleteSelectedEdge();
		}
	});
}

async function refresh() {
	const r = await api("/api/state");
	if (r.state) { state = r.state; render(); }
}

// ---------- ops ----------

async function applyOps(ops, opts = {}) {
	const r = await api("/api/ops", { ops });
	if (r.state) state = r.state;
	if (!r.ok) toast(r.error || "operation rejected", "error");
	if (r.ok && opts.toast) toast(opts.toast, "success", 1800);
	render();
	return r.ok;
}

async function save() {
	const r = await api("/api/save");
	if (r._status === 409) {
		conflictModal(r.conflicts || []);
		return;
	}
	if (r.state) state = r.state;
	if (r.ok) toast("saved " + (r.saved || []).map((p) => basename(p)).join(", "), "success");
	else toast(r.error || "save failed", "error");
	render();
}

function basename(p) { return p.split(/[\\/]/).pop(); }

function conflictModal(files) {
	modal(`
		<h3>Changed on disk</h3>
		<p style="color:var(--text-dim)">These files were modified outside the editor since they were loaded:</p>
		<pre>${esc(files.map(basename).join("\n"))}</pre>
		<div class="mrow">
			<button class="tbtn" id="mCancel">Keep editing</button>
			<button class="tbtn primary" id="mReload">Reload from disk</button>
		</div>`);
	$("mReload").onclick = async () => { closeModal(); const r = await api("/api/reload"); if (r.state) { state = r.state; toast("reloaded from disk", "success"); } render(); };
	$("mCancel").onclick = closeModal;
}

function modal(html) {
	closeModal();
	const ov = document.createElement("div");
	ov.className = "overlay";
	ov.id = "overlay";
	ov.innerHTML = `<div class="modal">${html}</div>`;
	ov.addEventListener("mousedown", (e) => { if (e.target === ov) closeModal(); });
	document.body.appendChild(ov);
}

function closeModal() { $("overlay")?.remove(); }

// ---------- render ----------

function render() {
	renderTabs();
	renderCards();
	// a debounced body commit re-renders while the user may still be typing;
	// rebuilding the textarea would steal focus and the caret
	const bodyFocused = document.activeElement?.id === "fBody";
	if (view === "graph") renderGraph();
	else if (!bodyFocused) renderEditor();
	renderLint();
	renderStatus();
	$("viewEdit").classList.toggle("active", view === "edit");
	$("viewGraph").classList.toggle("active", view === "graph");
}

function renderTabs() {
	const nav = $("filetabs");
	nav.innerHTML = "";
	for (const f of state.files) {
		const b = document.createElement("button");
		b.className = "filetab" + (f.path === activeFile ? " active" : "") + (isFileDirty(f) ? " dirty" : "");
		b.innerHTML = `<span class="dot"></span>${esc(basename(f.path))}${f.main ? "" : `<span class="imp">import</span>`}`;
		b.onclick = () => { activeFile = f.path; sel = null; edTab = "command"; render(); };
		nav.appendChild(b);
	}
}

function isFileDirty(f) {
	return !!f.dirty;
}

function renderCards() {
	const list = $("cmdlist");
	list.innerHTML = "";
	const f = activeFileState();
	if (!f) return;
	for (const c of f.commands) {
		if (filter && !(c.name + " " + (c.display || "") + " " + (c.description || "")).toLowerCase().includes(filter)) continue;
		list.appendChild(cardEl(f, c));
	}
	if (!f.commands.length && !f.parse_error) {
		list.innerHTML = `<div style="color:var(--text-faint);padding:20px 8px;text-align:center">no commands</div>`;
	}
}

function cardEl(f, c) {
	const el = document.createElement("div");
	el.className = "card" + (sel?.file === f.path && sel?.name === c.name ? " selected" : "");
	el.draggable = true;
	const h = c.header || {};
	let badges = "";
	if (h.is_default) badges += `<span class="badge default">default</span>`;
	if (h.cloud) badges += `<span class="badge cloud">cloud</span>`;
	if (h.manual) badges += `<span class="badge manual">manual</span>`;
	if (h.timeout) badges += `<span class="badge timeout">${esc(h.timeout)}</span>`;
	if (h.container) badges += `<span class="badge container">container</span>`;
	if ((h.arguments || []).length) badges += `<span class="badge args">${h.arguments.length} arg${h.arguments.length > 1 ? "s" : ""}</span>`;
	if (c.display && c.display !== c.name) badges += `<span class="badge ns" title="name as referenced from the main file">${esc(c.display)}</span>`;

	const deps = (h.prereqs || []).map((p) => `<span class="chip dep">${esc(p)}</span>`).join("");
	const files = (h.file_deps || []).map((p) => `<span class="chip file">${esc(p)}</span>`).join("");
	const prods = (h.produces || []).map((p) => `<span class="chip">${esc(p)}</span>`).join("");

	el.innerHTML = `
		<span class="grip">⠿</span>
		<div class="cbody">
			<div class="ctitle"><span class="nm">${esc(c.name)}</span></div>
			${c.description ? `<div class="cdesc">${esc(c.description)}</div>` : ""}
			<div class="chips">${badges}${prods}</div>
			<div class="prereqzone">${deps}${files}<span class="hint">${(h.prereqs || []).length + (h.file_deps || []).length ? "" : "drop a command here to add a prerequisite"}</span></div>
		</div>`;

	el.addEventListener("click", (e) => {
		if (e.target.closest(".prereqzone .chip")) return;
		sel = { file: f.path, name: c.name };
		edTab = "command";
		render();
	});

	// --- drag to reorder ---
	el.addEventListener("dragstart", (e) => {
		e.dataTransfer.setData("text/plain", JSON.stringify({ type: "cmd", file: f.path, name: c.name, line: c.line }));
		e.dataTransfer.effectAllowed = "move";
		el.classList.add("dragging");
	});
	el.addEventListener("dragend", () => el.classList.remove("dragging"));
	el.addEventListener("dragover", (e) => {
		const dt = readDrag(e);
		if (!dt || dt.type !== "cmd" || dt.file !== f.path) return;
		e.preventDefault();
		const r = el.getBoundingClientRect();
		const before = e.clientY < r.top + r.height / 2;
		showDropGap(list, el, before);
	});
	el.addEventListener("dragleave", () => hideDropGap());
	el.addEventListener("drop", async (e) => {
		const dt = readDrag(e);
		hideDropGap();
		if (!dt || dt.type !== "cmd" || dt.file !== f.path || dt.name === c.name) return;
		e.preventDefault();
		await applyOps([{ file: f.path, kind: "moveCommand", name: dt.name, before: c.name }], { toast: "moved" });
	});

	// --- drop target for wiring prereqs ---
	const zone = el.querySelector(".prereqzone");
	zone.addEventListener("dragover", (e) => {
		const dt = readDrag(e);
		if (!dt || dt.type !== "cmd" || dt.name === c.name) return;
		e.preventDefault();
		e.stopPropagation();
		zone.classList.add("over");
	});
	zone.addEventListener("dragleave", () => zone.classList.remove("over"));
	zone.addEventListener("drop", async (e) => {
		const dt = readDrag(e);
		zone.classList.remove("over");
		if (!dt || dt.type !== "cmd" || dt.name === c.name) return;
		e.preventDefault();
		e.stopPropagation();
		const spelling = prereqSpelling(f, dt);
		if (!spelling) {
			toast(`${dt.name} is not visible in ${basename(f.path)} — add an import first`, "error");
			return;
		}
		const h = c.header || {};
		const prereqs = (h.prereqs || []).slice();
		if (prereqs.includes(spelling) || (h.file_deps || []).includes(spelling)) return;
		prereqs.push(spelling);
		await applyOps([{ file: f.path, kind: "setHeader", name: c.name, header: { prereqs } }], { toast: `${c.name} < ${spelling}` });
	});

	return el;
}

function readDrag(e) {
	try { return JSON.parse(e.dataTransfer.getData("text/plain")); } catch { return null; }
}

// name of the dragged command as spelled inside file f (handles namespaces)
function prereqSpelling(f, dt) {
	if (dt.file === f.path) return dt.name;
	const v = (f.visible || []).find((v) => v.file === dt.file && v.line === dt.line);
	return v ? v.name : null;
}

function showDropGap(list, card, before) {
	hideDropGap();
	const gap = document.createElement("div");
	gap.className = "dropgap";
	gap.id = "dropgap";
	list.insertBefore(gap, before ? card : card.nextSibling);
}

function hideDropGap() { $("dropgap")?.remove(); }

// ---------- editor ----------

function renderEditor() {
	const pane = $("editorpane");
	const f = activeFileState();
	if (!f) { pane.innerHTML = ""; return; }

	let html = `<div class="etabs">
		<button class="etab ${edTab === "command" ? "active" : ""}" data-tab="command">Command</button>
		<button class="etab ${edTab === "glue" ? "active" : ""}" data-tab="glue">Variables & imports</button>
		<button class="etab ${edTab === "raw" ? "active" : ""}" data-tab="raw">Raw</button>
	</div>`;
	if (f.parse_error && edTab !== "raw") {
		html += `<div class="parseerr" style="margin:14px 22px 0"><span>parse error</span><span style="flex:1">${esc(f.parse_error)}</span></div>`;
	}
	if (edTab === "command") html += commandFormHTML(f);
	else if (edTab === "glue") html += glueHTML(f);
	else html += rawHTML(f);

	pane.innerHTML = html;

	pane.querySelectorAll(".etab").forEach((b) => {
		b.onclick = () => { edTab = b.dataset.tab; render(); };
	});

	if (edTab === "command") bindCommandForm(f);
	else if (edTab === "glue") bindGlue(f);
	else bindRaw(f);
}

const SNIPPETS = [
	["if", 'if "&x" == "y" {\n    \n}'],
	["if / else", 'if "&x" == "y" {\n    \n} else {\n    \n}'],
	["for", "for f in a, b {\n    $ \n}"],
	["for glob", "for f in *.go {\n    $ \n}"],
	["parallel for", "parallel for x in a, b {\n    $ \n}"],
	["env", "env { KEY=value }\n$ "],
	["invoke", "invoke other"],
	["retry", "retry<3> $ "],
	["in dir", "in subdir {\n    $ \n}"],
	["onfail", "onfail {\n    $ cleanup\n}"],
	["confirm", 'confirm "proceed?"'],
	["output", '$ echo "named" as out'],
];

function commandFormHTML(f) {
	const c = selCommand();
	if (!c) {
		return `<div style="padding:40px;color:var(--text-faint);text-align:center">select a command on the left<br><span style="font-size:11.5px">drag cards to reorder · drop one onto a prereq area to connect</span></div>`;
	}
	const h = c.header || {};
	const args = (h.arguments || []).map((a, i) => `
		<div class="argrow" data-i="${i}">
			<input class="aname" value="${esc(a.name)}" placeholder="name">
			<label style="display:flex;gap:4px;align-items:center;font-size:11px;color:var(--text-dim)"><input type="checkbox" class="aopt" ${a.is_optional ? "checked" : ""}>opt</label>
			<input class="adflt dflt" value="${esc(a.default || "")}" placeholder="default">
			<button class="rm" title="remove">✕</button>
		</div>`).join("");

	const prereqChips = (h.prereqs || []).map((p) => chipFor(f, c, p, "dep")).join("");
	const fileChips = (h.file_deps || []).map((p) => chipFor(f, c, p, "file")).join("");

	return `
	<div class="ebody">
		<div class="formgrid">
			<label>name</label>
			<div class="row"><input id="fName" value="${esc(h.is_default ? "_" : c.name)}" style="font-family:var(--mono)"></div>

			<label>markers</label>
			<div class="flags">
				<label><input type="checkbox" id="fDefault" ${h.is_default ? "checked" : ""}> default <span style="color:var(--text-faint)">(_)</span></label>
				<label><input type="checkbox" id="fCloud" ${h.cloud ? "checked" : ""}> cloud <span style="color:var(--text-faint)">(|name|)</span></label>
				<label><input type="checkbox" id="fManual" ${h.manual ? "checked" : ""}> manual</label>
			</div>

			<label>arguments</label>
			<div class="row" style="flex-direction:column;align-items:stretch" id="argRows">${args || '<span style="color:var(--text-faint);font-size:12px">none</span>'}</div>

			<label>prerequisites</label>
			<div class="row" id="prereqRow">${prereqChips || '<span style="color:var(--text-faint);font-size:12px">none</span>'}
				<input id="fAddPrereq" placeholder="+ add" list="visibleNames" style="width:110px;font-family:var(--mono);font-size:11.5px">
				<datalist id="visibleNames">${(f.visible || []).concat(f.commands.map((x) => ({ name: x.name }))).map((v) => `<option value="${esc(v.name)}">`).join("")}</datalist>
			</div>

			<label>file deps</label>
			<div class="row" id="fdepRow">${fileChips || '<span style="color:var(--text-faint);font-size:12px">none</span>'}
				<input id="fAddFdep" placeholder="+ add path" style="width:130px;font-family:var(--mono);font-size:11.5px">
			</div>

			<label>produces</label>
			<div class="row"><input id="fProduces" value="${esc((h.produces || []).join(", "))}" placeholder="dist/app, …" style="width:100%;font-family:var(--mono);font-size:12px"></div>

			<label>onchange</label>
			<div class="row"><input id="fOnchange" value="${esc((h.onchange || []).join(", "))}" placeholder="src/**.c, …" style="width:100%;font-family:var(--mono);font-size:12px"></div>

			<label>container</label>
			<div class="row"><input id="fContainer" value="${esc(h.container || "")}" placeholder="golang:1.26" style="font-family:var(--mono);font-size:12px"></div>

			<label>timeout</label>
			<div class="row"><input id="fTimeout" value="${esc(h.timeout || "")}" placeholder="120s" style="width:100px;font-family:var(--mono);font-size:12px"></div>

			<label>workdir</label>
			<div class="row"><input id="fWorkdir" value="${esc(h.work_dir || "")}" placeholder="src" style="width:160px;font-family:var(--mono);font-size:12px"></div>
		</div>

		<div class="bodywrap">
			<div style="display:flex;align-items:center;gap:10px;margin:16px 0 6px">
				<span style="color:var(--text-dim);font-size:12.5px">body</span>
				${c.end_line > c.line + 1 ? `<span style="color:var(--text-faint);font-size:11px">lines ${c.line + 1}–${c.end_line - 1}</span>` : ""}
				${(c.stmts || []).length ? `<div class="viewtoggle" style="margin-left:auto">
					<button id="bmCode" class="${bodyMode === "code" ? "active" : ""}">Code</button>
					<button id="bmStruct" class="${bodyMode === "structure" ? "active" : ""}">Structure</button>
				</div>` : ""}
			</div>
			${bodyMode === "structure" && (c.stmts || []).length ? stmtsHTML(c) : codeEditorHTML("fBody", c.body)}
			${bodyMode === "code" || !(c.stmts || []).length ? `<div class="palette">${SNIPPETS.map((s, i) => `<button class="snippet" draggable="true" data-i="${i}">${esc(s[0])}</button>`).join("")}</div>` : `<div class="palette" style="color:var(--text-faint);font-size:11px">drag statements to reorder — drop onto “nest inside” to move them into a block · click a row to jump to its code</div>`}
			<div class="cmdactions">
				<button class="tbtn" id="btnDup">Duplicate</button>
				<button class="tbtn" id="btnDelete" style="color:var(--red)">Delete</button>
				<button class="tbtn" id="btnDry">⚡ Dry-run</button>
			</div>
		</div>
	</div>`;
}

const STMT_KIND_LABEL = { parallel: "parallel", builtin: "builtin", onfail: "onfail", invoke: "invoke", shell: "shell", env: "env", if: "if", switch: "switch", for: "for", in: "in", lock: "lock" };

function stmtsHTML(c) {
	const row = (s) => {
		const kids = (s.children || []).map(row).join("");
		const kind = STMT_KIND_LABEL[s.type] || s.type;
		const container = s.close > 0 || (s.children || []).length > 0;
		return `<div class="stmt" data-line="${s.line}" data-close="${s.close || 0}" draggable="true">
			<div class="srow" data-line="${s.line}">
				<span class="s-handle">⠿</span>
				<span class="s-kind ${esc(kind)}">${esc(kind)}</span>
				<span class="s-sum" title="line ${s.line}">${esc(s.summary || "")}</span>
				<button class="s-del" title="delete statement">✕</button>
			</div>
			${kids ? `<div class="s-kids">${kids}</div>` : ""}
			${container ? `<div class="s-into" data-close="${s.close || 0}" title="drop a statement here to nest it inside this block">⤵ nest inside</div>` : ""}
		</div>`;
	};
	return `<div class="stmts" id="stmtOutline">${(c.stmts || []).map(row).join("") || '<div style="color:var(--text-faint);padding:12px">empty body</div>'}</div>`;
}

function chipFor(f, c, name, cls) {
	const dir = (c.header.prereq_dirs || {})[name];
	return `<span class="chip ${cls}" data-name="${esc(name)}" ${dir ? `title="runs in ${esc(dir)}" data-dir="${esc(dir)}"` : ""}>${esc(name)}${dir ? ` <span style="opacity:.6">in ${esc(dir)}</span>` : ""}<span class="x" data-x="1">✕</span></span>`;
}

function bindCommandForm(f) {
	const c = selCommand();
	if (!c) return;

	const commit = (mut) => {
		const h = JSON.parse(JSON.stringify(c.header || {}));
		mut(h);
		applyOps([{ file: f.path, kind: "setHeader", name: c.name, header: h }]);
	};

	$("fName").onchange = () => {
		const v = $("fName").value.trim();
		if (!v) return;
		commit((h) => { h.name = v; h.is_default = v === "_"; });
		sel = { file: f.path, name: v === "_" ? "_" : v };
	};
	$("fDefault").onchange = () => commit((h) => { h.is_default = $("fDefault").checked; });
	$("fCloud").onchange = () => commit((h) => { h.cloud_accessible = $("fCloud").checked; });
	$("fManual").onchange = () => commit((h) => { h.manual = $("fManual").checked; });
	$("fProduces").onchange = () => commit((h) => { h.produces = splitList($("fProduces").value); });
	$("fOnchange").onchange = () => commit((h) => { h.onchange = splitList($("fOnchange").value); });
	$("fContainer").onchange = () => commit((h) => { h.container = $("fContainer").value.trim(); });
	$("fTimeout").onchange = () => commit((h) => { h.timeout = $("fTimeout").value.trim(); });
	$("fWorkdir").onchange = () => commit((h) => { h.work_dir = $("fWorkdir").value.trim(); });

	const bindArgRows = () => {
		const rows = document.querySelectorAll("#argRows .argrow");
		rows.forEach((row, i) => {
			row.querySelector(".rm").onclick = () => {
				commit((h) => { h.arguments = (h.arguments || []).filter((_, j) => j !== i); });
			};
			row.querySelector(".aname").onchange = (e) => commit((h) => { h.arguments[i].name = e.target.value.trim(); });
			row.querySelector(".aopt").onchange = (e) => commit((h) => { h.arguments[i].is_optional = e.target.checked; });
			row.querySelector(".adflt").onchange = (e) => commit((h) => { h.arguments[i].default = e.target.value.trim(); });
		});
	};
	bindArgRows();
	const addArgBtn = document.createElement("button");
	addArgBtn.className = "addlink";
	addArgBtn.textContent = "+ argument";
	addArgBtn.onclick = () => commit((h) => {
		h.arguments = h.arguments || [];
		h.arguments.push({ name: "arg" + (h.arguments.length + 1), is_optional: false, default: "" });
	});
	$("argRows").appendChild(addArgBtn);

	const wireChipRemoval = (rowId, field) => {
		document.querySelectorAll(`#${rowId} .chip .x`).forEach((x) => {
			x.onclick = () => {
				const name = x.closest(".chip").dataset.name;
				commit((h) => { h[field] = (h[field] || []).filter((p) => p !== name); });
			};
		});
	};
	wireChipRemoval("prereqRow", "prereqs");
	wireChipRemoval("fdepRow", "file_deps");

	$("fAddPrereq").onchange = () => {
		const v = $("fAddPrereq").value.trim();
		if (!v) return;
		commit((h) => { h.prereqs = (h.prereqs || []); if (!h.prereqs.includes(v)) h.prereqs.push(v); });
	};
	$("fAddFdep").onchange = () => {
		const v = $("fAddFdep").value.trim();
		if (!v) return;
		commit((h) => { h.file_deps = (h.file_deps || []); if (!h.file_deps.includes(v)) h.file_deps.push(v); });
	};

	const ta = $("fBody");
	if (ta) {
		wireCodeEditor(ta);
		ta.addEventListener("input", () => {
			clearTimeout(bodyTimer);
			bodyTimer = setTimeout(() => commitBody(f, c), 700);
		});
		ta.addEventListener("blur", () => { clearTimeout(bodyTimer); commitBody(f, c); });

		document.querySelectorAll(".snippet").forEach((b) => {
			const snip = SNIPPETS[+b.dataset.i][1];
			b.onclick = () => insertAtCaret(ta, snip);
			b.addEventListener("dragstart", (e) => e.dataTransfer.setData("text/plain", snip));
		});
		ta.addEventListener("drop", (e) => {
			const t = e.dataTransfer.getData("text/plain");
			if (!t) return;
			e.preventDefault();
			insertAtCaret(ta, t);
		});
	}

	$("bmCode") && ($("bmCode").onclick = () => { bodyMode = "code"; render(); });
	$("bmStruct") && ($("bmStruct").onclick = () => { bodyMode = "structure"; render(); });

	const selKey = f.path + "#" + c.name;
	if (lastBodySel !== selKey) { bodyMode = "code"; lastBodySel = selKey; }
	bindStructure(f, c);

	$("btnDup").onclick = () => applyOps([{ file: f.path, kind: "duplicateCommand", name: c.name }], { toast: "duplicated" });
	$("btnDelete").onclick = () => {
		if (!confirm(`Delete command ${c.name}?`)) return;
		sel = null;
		applyOps([{ file: f.path, kind: "deleteCommand", name: c.name }], { toast: "deleted" });
	};
	$("btnDry").onclick = () => dryRun([c.name]);
}

function commitBody(f, c) {
	const ta = $("fBody");
	if (!ta) return;
	applyOps([{ file: f.path, kind: "setBody", name: c.name, body: ta.value }]);
}

function bindStructure(f, c) {
	const outline = $("stmtOutline");
	if (!outline) return;

	outline.querySelectorAll(".stmt").forEach((el) => {
		const line = +el.dataset.line;

		el.addEventListener("dragstart", (e) => {
			e.dataTransfer.setData("text/plain", JSON.stringify({ type: "stmt", line }));
			e.dataTransfer.effectAllowed = "move";
			el.classList.add("dragging");
		});
		el.addEventListener("dragend", () => el.classList.remove("dragging"));

		const clear = () => el.classList.remove("dropbefore", "dropafter");
		el.addEventListener("dragover", (e) => {
			const dt = readDrag(e);
			if (!dt || dt.type !== "stmt" || dt.line === line) return;
			e.preventDefault();
			const r = el.getBoundingClientRect();
			const before = e.clientY < r.top + r.height / 2;
			clear();
			el.classList.add(before ? "dropbefore" : "dropafter");
		});
		el.addEventListener("dragleave", clear);
		el.addEventListener("drop", async (e) => {
			const dt = readDrag(e);
			const before = el.classList.contains("dropbefore");
			clear();
			if (!dt || dt.type !== "stmt" || dt.line === line) return;
			e.preventDefault();
			let at;
			if (before) {
				at = line;
			} else {
				const next = nextStmtSibling(el);
				at = next ? +next.dataset.line : containerBoundary(el, c);
			}
			await applyOps([{ file: f.path, kind: "moveStmt", name: c.name, line: dt.line, at }], { toast: "statement moved" });
		});

		el.querySelector(".s-del").onclick = () =>
			applyOps([{ file: f.path, kind: "deleteStmt", name: c.name, line }], { toast: "statement removed" });

		el.querySelector(".srow").addEventListener("click", (e) => {
			if (e.target.closest(".s-del")) return;
			jumpToStmt(f, c, line);
		});
	});

	outline.querySelectorAll(".s-into").forEach((zone) => {
		const close = +zone.dataset.close;
		zone.addEventListener("dragover", (e) => {
			const dt = readDrag(e);
			if (!dt || dt.type !== "stmt" || close <= 0) return;
			e.preventDefault();
			e.stopPropagation();
			zone.classList.add("over");
		});
		zone.addEventListener("dragleave", () => zone.classList.remove("over"));
		zone.addEventListener("drop", async (e) => {
			const dt = readDrag(e);
			zone.classList.remove("over");
			if (!dt || dt.type !== "stmt" || close <= 0) return;
			e.preventDefault();
			e.stopPropagation();
			await applyOps([{ file: f.path, kind: "moveStmt", name: c.name, line: dt.line, at: close }], { toast: "nested" });
		});
	});
}

function nextStmtSibling(el) {
	let n = el.nextElementSibling;
	return n && n.classList.contains("stmt") ? n : null;
}

function containerBoundary(el, c) {
	const parent = el.parentElement ? el.parentElement.closest(".stmt") : null;
	return parent ? +parent.dataset.close : c.end_line;
}

function jumpToStmt(f, c, line) {
	bodyMode = "code";
	render();
	const ta = $("fBody");
	if (!ta) return;
	const bodyIndex = Math.max(0, line - (c.line + 1));
	const lines = ta.value.split("\n");
	let off = 0;
	for (let i = 0; i < bodyIndex && i < lines.length; i++) off += lines[i].length + 1;
	ta.focus();
	ta.selectionStart = off;
	ta.selectionEnd = lines[bodyIndex] ? off + lines[bodyIndex].length : off;
	ta.scrollTop = Math.max(0, bodyIndex * 20.8 - 80);
}

function insertAtCaret(ta, text) {
	const s = ta.selectionStart, e = ta.selectionEnd;
	let ok = false;
	try { ok = document.execCommand("insertText", false, text); } catch { ok = false; }
	if (!ok) {
		ta.value = ta.value.slice(0, s) + text + ta.value.slice(e);
		ta.dispatchEvent(new Event("input", { bubbles: true }));
	}
	ta.selectionStart = ta.selectionEnd = s + text.length;
	ta.focus();
}

function splitList(v) {
	const out = v.split(",").map((s) => s.trim()).filter(Boolean);
	return out.length ? out : [];
}

// ---------- glue ----------

function glueHTML(f) {
	const spans = f.commands.map((c) => [c.doc_start, c.end_line]);
	const lines = f.text.split("\n");
	let rows = "";
	for (let n = 1; n <= lines.length; n++) {
		if (spans.some(([a, b]) => n >= a && n <= b)) continue;
		const t = lines[n - 1];
		if (t.trim() === "" && n === lines.length) continue;
		const kind = t.trimStart().startsWith("var ") ? "var"
			: t.trimStart().startsWith("state ") ? "state"
			: t.trimStart().startsWith("import ") ? "import"
			: t.trimStart().startsWith("#") || t.trimStart().startsWith("//") ? "comment" : "none";
		rows += `
		<div class="gluerow" data-line="${n}">
			<span class="ln">${n}</span>
			<span class="kind ${kind}">${kind}</span>
			<input value="${esc(t)}" spellcheck="false">
			<button class="rm" title="delete line">✕</button>
		</div>`;
	}
	return `<div class="ebody"><div class="glue">${rows}
		<div style="margin-top:8px"><button class="addlink" id="glueAdd">+ line (var / state / import …)</button></div>
		<p style="color:var(--text-faint);font-size:11.5px;margin-top:14px">these are the lines between commands — variables keep their raw expression text</p>
	</div></div>`;
}

function bindGlue(f) {
	document.querySelectorAll(".gluerow").forEach((row) => {
		const n = +row.dataset.line;
		const input = row.querySelector("input");
		input.onchange = () => applyOps([{ file: f.path, kind: "setLine", line: n, text: input.value }]);
		row.querySelector(".rm").onclick = () => applyOps([{ file: f.path, kind: "deleteLines", line: n, end_line: n }], { toast: "line removed" });
	});
	$("glueAdd").onclick = () => {
		modal(`
			<h3>Add a line</h3>
			<div class="field"><label>content</label><input type="text" id="glueText" placeholder='var name = value   ·   import "lib.constfile" as lib   ·   state x = "1"'></div>
			<div class="mrow"><button class="tbtn" id="mCancel">Cancel</button><button class="tbtn primary" id="mOk">Add</button></div>`);
		$("glueText").focus();
		$("mCancel").onclick = closeModal;
		$("mOk").onclick = async () => {
			const t = $("glueText").value;
			closeModal();
			if (!t.trim()) return;
			await applyOps([{ file: f.path, kind: "insertLines", line: 0, text: t }], { toast: "line added" });
		};
	};
}

// ---------- raw ----------

function rawHTML(f) {
	return `<div class="ebody"><div class="bodywrap" style="max-width:1000px">
		${codeEditorHTML("rawText", f.text, "58vh")}
		<div class="cmdactions"><button class="tbtn primary" id="rawApply">Apply</button></div>
	</div></div>`;
}

function bindRaw(f) {
	wireCodeEditor($("rawText"));
	$("rawApply").onclick = () => applyOps([{ file: f.path, kind: "setFile", text: $("rawText").value }], { toast: "applied" });
}

// ---------- graph ----------

function renderGraph() {
	const f = activeFileState();
	const pane = $("editorpane");
	if (!f) { pane.innerHTML = ""; return; }

	const nodes = [];
	const index = new Map();
	const addNode = (name, file, main, cmd) => {
		if (index.has(name)) return;
		index.set(name, nodes.length);
		nodes.push({ name, file, main, cmd, edges: [] });
	};
	for (const c of f.commands) addNode(c.name, f.path, true, c);
	for (const v of f.visible || []) addNode(v.name, v.file, false, null);

	for (const c of f.commands) {
		for (const p of (c.header?.prereqs || [])) {
			if (index.has(p)) nodes[index.get(c.name)].edges.push({ to: p });
		}
	}

	const depth = new Map();
	const compute = (name, seen) => {
		if (depth.has(name)) return depth.get(name);
		if (seen.has(name)) return 0;
		seen.add(name);
		let d = 0;
		for (const e of nodes[index.get(name)].edges) d = Math.max(d, compute(e.to, seen) + 1);
		seen.delete(name);
		depth.set(name, d);
		return d;
	};
	for (const n of nodes) compute(n.name, new Set());

	const layers = [];
	for (const n of nodes) {
		const d = depth.get(n.name);
		(layers[d] = layers[d] || []).push(n);
	}
	const W = 178, H = 44, GX = 68, GY = 26;
	layers.forEach((layer, li) => {
		layer.forEach((n, ni) => {
			if (!graphPos.has(n.name)) graphPos.set(n.name, { x: li * (W + GX) + 10, y: ni * (H + GY) + 10 });
		});
	});
	const width = Math.max(400, ...[...graphPos.values()].map((p) => p.x + W + 40));
	const height = Math.max(280, ...[...graphPos.values()].map((p) => p.y + H + 40));

	let svg = `<svg id="graphSvg" width="${width}" height="${height}" xmlns="http://www.w3.org/2000/svg">`;
	let handleLayer = "";
	for (const n of nodes) {
		for (const e of n.edges) {
			const a = graphPos.get(n.name), b = graphPos.get(e.to);
			if (!a || !b) continue;
			const d = edgeD(n.name, e.to);
			const isSel = selectedEdge && selectedEdge.from === n.name && selectedEdge.to === e.to;
			const ax = a.x + W / 2, ay = a.y + H, bx = b.x + W / 2, by = b.y;
			svg += `<g class="gedge ${isSel ? "selected" : ""}" data-from="${esc(n.name)}" data-to="${esc(e.to)}">
				<path class="hit" d="${d}"/>
				<path class="vis" d="${d}"/>
			</g>`;
			// connector dots render above the nodes: they sit exactly on the
			// node borders and would be unreachable under the node rects
			handleLayer += `<circle class="ehandle" data-from="${esc(n.name)}" data-to="${esc(e.to)}" data-end="from" cx="${ax}" cy="${ay}" r="5"><title>drag to move this dependency to another command</title></circle>
				<circle class="ehandle" data-from="${esc(n.name)}" data-to="${esc(e.to)}" data-end="to" cx="${bx}" cy="${by}" r="5"><title>drag to re-target this dependency</title></circle>`;
			for (const w of (edgeWaypoints.get(edgeKey(n.name, e.to)) || [])) {
				handleLayer += `<circle class="whandle" data-from="${esc(n.name)}" data-to="${esc(e.to)}" cx="${w.x}" cy="${w.y}" r="4.5"><title>drag to reshape · double-click to remove</title></circle>`;
			}
		}
	}
	for (const n of nodes) {
		const p = graphPos.get(n.name);
		const cls = ["gnode", n.main ? "main" : "", sel?.name === n.name ? "selected" : ""].join(" ");
		svg += `<g class="${cls}" data-name="${esc(n.name)}" data-main="${n.main}" transform="translate(${p.x},${p.y})">
			<rect class="nbox" width="${W}" height="${H}" rx="9"/>
			<text x="${W / 2}" y="${H / 2}">${esc(n.name)}</text>
			${n.main ? `<circle class="nhandle" cx="${W}" cy="${H / 2}" r="6" title="drag to connect"/>` : ""}
		</g>`;
	}
	svg += `<g class="ehlayer">${handleLayer}</g></svg>`;

	pane.innerHTML = `<div class="etabs"><button class="etab active">Dependency graph — ${esc(basename(f.path))}</button></div>
		<div class="graphwrap" id="graphwrap">${svg}</div>
		<div class="statusbar" style="border-top:1px solid var(--border)">
			<span class="s">drag ● to connect · grab an edge's end dot to re-point it · drag an edge's body to bend it (double-click straightens) · click + Delete removes · drag nodes to arrange</span>
			${selectedEdge ? `<span class="s warn">edge ${esc(selectedEdge.from)} → ${esc(selectedEdge.to)} selected</span>` : ""}
		</div>`;

	bindGraph(pane, f, nodes);
	graphFit($("graphSvg"), $("graphwrap"));
}

// graphFit grows the SVG canvas so it always covers every node (plus the
// given point) and at least fills the visible pane — nodes stay visible
// when dragged toward or past the current edge.
function graphFit(svg, wrap, extraPt) {
	if (!svg || !wrap) return;
	let maxW = 0, maxH = 0;
	for (const p of graphPos.values()) {
		maxW = Math.max(maxW, p.x + 178);
		maxH = Math.max(maxH, p.y + 44);
	}
	for (const wps of edgeWaypoints.values()) {
		for (const p of wps) {
			maxW = Math.max(maxW, p.x + 20);
			maxH = Math.max(maxH, p.y + 20);
		}
	}
	if (extraPt) {
		maxW = Math.max(maxW, extraPt.x + 20);
		maxH = Math.max(maxH, extraPt.y + 20);
	}
	const w = Math.max(maxW + 40, wrap.clientWidth);
	const h = Math.max(maxH + 40, wrap.clientHeight);
	if (w > +svg.getAttribute("width")) svg.setAttribute("width", w);
	if (h > +svg.getAttribute("height")) svg.setAttribute("height", h);
}

function svgCoords(svg, cx, cy) {
	const pt = new DOMPoint(cx, cy);
	return pt.matrixTransform(svg.getScreenCTM().inverse());
}

function bindGraph(pane, f, nodes) {
	const wrap = $("graphwrap");
	const svg = $("graphSvg");
	const byName = new Map(nodes.map((n) => [n.name, n]));
	const nodeEls = new Map([...pane.querySelectorAll(".gnode")].map((el) => [el.dataset.name, el]));

	// node click / dblclick
	for (const [name, el] of nodeEls) {
		el.addEventListener("click", () => {
			if (graphClickGuard) return;
			selectedEdge = null;
			if (el.dataset.main === "true") { sel = { file: f.path, name }; }
			else toast(`${name} is defined in another file — open it from the tabs above`, "", 2400);
			render();
		});
	}

	// edge select happens on pointerup without movement (see endDrag);
	// dragging an edge body bends it by pulling out a waypoint

	// node dragging (arrange), connect handle, edge-end rewiring, edge bending
	let drag = null;
	wrap.addEventListener("pointerdown", (e) => {
		const ehandle = e.target.closest(".ehandle");
		if (ehandle && e.button === 0) {
			drag = { mode: "rewire", from: ehandle.dataset.from, to: ehandle.dataset.to, end: ehandle.dataset.end };
			svg.querySelector(`.gedge[data-from="${ehandle.dataset.from}"][data-to="${ehandle.dataset.to}"]`)?.classList.add("rewiring");
			wrap.classList.add("dragging");
			e.preventDefault();
			return;
		}
		const whandle = e.target.closest(".whandle");
		if (whandle && e.button === 0) {
			drag = { mode: "waypoint", from: whandle.dataset.from, to: whandle.dataset.to, wp: +whandle.dataset.wp };
			wrap.classList.add("dragging");
			e.preventDefault();
			return;
		}
		const edgeEl = e.target.closest(".gedge");
		if (edgeEl && e.button === 0) {
			drag = { mode: "edgeGrab", from: edgeEl.dataset.from, to: edgeEl.dataset.to, startX: e.clientX, startY: e.clientY };
			wrap.classList.add("dragging");
			e.preventDefault();
			return;
		}
		const handle = e.target.closest(".nhandle");
		const nodeEl = e.target.closest(".gnode");
		if (!nodeEl) return;
		const p = graphPos.get(nodeEl.dataset.name);
		if (!p) return;
		const pt = svgCoords(svg, e.clientX, e.clientY);
		if (handle) {
			drag = { mode: "connect", from: nodeEl.dataset.name };
		} else if (e.button === 0 && e.target.closest(".nbox")) {
			drag = { mode: "move", name: nodeEl.dataset.name, dx: pt.x - p.x, dy: pt.y - p.y, moved: false };
		} else return;
		wrap.classList.add("dragging");
		e.preventDefault();
	});
	const tempLine = () => {
		let temp = svg.querySelector(".gtempline");
		if (!temp) {
			temp = document.createElementNS("http://www.w3.org/2000/svg", "path");
			temp.setAttribute("class", "gtempline");
			svg.appendChild(temp);
		}
		return temp;
	};
	wrap.addEventListener("pointermove", (e) => {
		if (!drag) return;
		const pt = svgCoords(svg, e.clientX, e.clientY);
		if (drag.mode === "move") {
			const p = graphPos.get(drag.name);
			p.x = Math.max(4, pt.x - drag.dx);
			p.y = Math.max(4, pt.y - drag.dy);
			drag.moved = true;
			const el = nodeEls.get(drag.name);
			el.setAttribute("transform", `translate(${p.x},${p.y})`);
			redrawEdges(pane, f);
			graphFit(svg, wrap);
		} else if (drag.mode === "connect") {
			const from = graphPos.get(drag.from);
			tempLine().setAttribute("d", `M ${from.x + 178} ${from.y + 22} L ${pt.x} ${pt.y}`);
			graphFit(svg, wrap, pt);
		} else if (drag.mode === "rewire") {
			// the grabbed end follows the cursor; the other stays anchored
			if (drag.end === "to") {
				const from = graphPos.get(drag.from);
				tempLine().setAttribute("d", `M ${from.x + 89} ${from.y + 44} L ${pt.x} ${pt.y}`);
			} else {
				const to = graphPos.get(drag.to);
				tempLine().setAttribute("d", `M ${pt.x} ${pt.y} L ${to.x + 89} ${to.y}`);
			}
			graphFit(svg, wrap, pt);
		} else if (drag.mode === "edgeGrab") {
			// pulling the edge body past a small threshold bends out a waypoint
			if (Math.hypot(e.clientX - drag.startX, e.clientY - drag.startY) < 5) return;
			const key = edgeKey(drag.from, drag.to);
			const wps = edgeWaypoints.get(key) || [];
			wps.push({ x: pt.x, y: pt.y });
			edgeWaypoints.set(key, wps);
			drag = { mode: "waypoint", from: drag.from, to: drag.to, wp: wps.length - 1 };
			syncEdgeVisuals(svg, drag.from, drag.to);
		} else if (drag.mode === "waypoint") {
			const key = edgeKey(drag.from, drag.to);
			const wps = edgeWaypoints.get(key) || [];
			wps[drag.wp] = { x: pt.x, y: pt.y };
			edgeWaypoints.set(key, wps);
			syncEdgeVisuals(svg, drag.from, drag.to);
			graphFit(svg, wrap, pt);
		}
	});
	const endDrag = async (e) => {
		if (!drag) return;
		wrap.classList.remove("dragging");
		svg.querySelector(".gtempline")?.remove();
		pane.querySelectorAll(".gedge.rewiring").forEach((g) => g.classList.remove("rewiring"));
		const mode = drag.mode;
		const moved = !!drag.moved;
		const from = drag.from || drag.name;
		const edgeTo = drag.to;
		const edgeEnd = drag.end;
		drag = null;
		if (mode === "rewire") {
			const target = document.elementFromPoint(e.clientX, e.clientY)?.closest(".gnode");
			if (!target) return;
			await rewireEdge(f, { from, to: edgeTo }, edgeEnd, target.dataset.name);
			return;
		}
		if (mode === "edgeGrab") {
			// released without dragging: toggle selection
			selectedEdge = (selectedEdge?.from === from && selectedEdge.to === edgeTo)
				? null : { from, to: edgeTo };
			render();
			return;
		}
		if (mode !== "connect") {
			if (moved) {
				graphClickGuard = true;
				setTimeout(() => { graphClickGuard = false; }, 0);
			}
			return;
		}
		const target = document.elementFromPoint(e.clientX, e.clientY)?.closest(".gnode");
		if (!target || target.dataset.name === from) return;
		await connectPrereq(f, from, target.dataset.name);
	};
	wrap.addEventListener("pointerup", endDrag);
	wrap.addEventListener("pointerleave", (e) => { if (drag) endDrag(e); });

	// double-click: on a waypoint dot it removes it, on an edge body it
	// straightens the edge back to the automatic route
	wrap.addEventListener("dblclick", (e) => {
		const wh = e.target.closest(".whandle");
		if (wh) {
			const key = edgeKey(wh.dataset.from, wh.dataset.to);
			const wps = edgeWaypoints.get(key) || [];
			wps.splice(+wh.dataset.wp, 1);
			edgeWaypoints.set(key, wps);
			syncEdgeVisuals(svg, wh.dataset.from, wh.dataset.to);
			return;
		}
		const edgeEl = e.target.closest(".gedge");
		if (edgeEl) {
			edgeWaypoints.delete(edgeKey(edgeEl.dataset.from, edgeEl.dataset.to));
			syncEdgeVisuals(svg, edgeEl.dataset.from, edgeEl.dataset.to);
		}
	});

	if (graphResizeHandler) window.removeEventListener("resize", graphResizeHandler);
	graphResizeHandler = () => graphFit($("graphSvg"), $("graphwrap"));
	window.addEventListener("resize", graphResizeHandler);
}

function redrawEdges(pane, f) {
	const W = 178, H = 44;
	const svg = $("graphSvg");
	pane.querySelectorAll(".gedge").forEach((g) => {
		const d = edgeD(g.dataset.from, g.dataset.to);
		if (d) g.querySelectorAll("path").forEach((p) => p.setAttribute("d", d));
	});
	svg?.querySelectorAll(".ehandle").forEach((h) => {
		const a = graphPos.get(h.dataset.from), b = graphPos.get(h.dataset.to);
		if (!a || !b) return;
		if (h.dataset.end === "from") {
			h.setAttribute("cx", a.x + W / 2);
			h.setAttribute("cy", a.y + H);
		} else {
			h.setAttribute("cx", b.x + W / 2);
			h.setAttribute("cy", b.y);
		}
	});
}

async function connectPrereq(f, from, to) {
	const cmd = f.commands.find((c) => c.name === from);
	if (!cmd) {
		toast(`${from} is not defined in ${basename(f.path)} — connect from a node in this file`, "error");
		return;
	}
	const prereqs = (cmd.header?.prereqs || []).slice();
	if (prereqs.includes(to) || (cmd.header?.file_deps || []).includes(to)) {
		toast(`${from} already depends on ${to}`);
		return;
	}
	prereqs.push(to);
	const ok = await applyOps([{ file: f.path, kind: "setHeader", name: from, header: { prereqs } }], { toast: `${from} < ${to}` });
	if (ok) selectedEdge = { from, to };
}

// rewireEdge re-points one end of an existing dependency edge: end === "to"
// changes which command it depends on, end === "from" moves the dependency
// to a different command. Both go through the validation gate.
async function rewireEdge(f, edge, end, targetName) {
	if (targetName === edge.from || targetName === edge.to) return;
	edgeWaypoints.delete(edgeKey(edge.from, edge.to));
	if (end === "to") {
		const cmd = f.commands.find((c) => c.name === edge.from);
		if (!cmd) {
			toast(`${edge.from} is not defined in ${basename(f.path)} — edit its dependency there`, "error");
			return;
		}
		const prereqs = (cmd.header?.prereqs || []).filter((p) => p !== edge.to);
		if (!prereqs.includes(targetName) && !(cmd.header?.file_deps || []).includes(targetName)) {
			prereqs.push(targetName);
		}
		const ok = await applyOps([{ file: f.path, kind: "setHeader", name: edge.from, header: { prereqs } }], { toast: `${edge.from} < ${targetName}` });
		if (ok) selectedEdge = { from: edge.from, to: targetName };
		return;
	}
	const src = f.commands.find((c) => c.name === edge.from);
	const dst = f.commands.find((c) => c.name === targetName);
	if (!src || !dst) {
		toast(`both ends of the edge must be commands defined in ${basename(f.path)}`, "error");
		return;
	}
	const srcPrereqs = (src.header?.prereqs || []).filter((p) => p !== edge.to);
	const dstPrereqs = (dst.header?.prereqs || []).slice();
	if (!dstPrereqs.includes(edge.to)) dstPrereqs.push(edge.to);
	const ops = [
		{ file: f.path, kind: "setHeader", name: edge.from, header: { prereqs: srcPrereqs } },
		{ file: f.path, kind: "setHeader", name: targetName, header: { prereqs: dstPrereqs } },
	];
	const ok = await applyOps(ops, { toast: `${targetName} < ${edge.to}` });
	if (ok) selectedEdge = { from: targetName, to: edge.to };
}

async function deleteSelectedEdge() {
	if (!selectedEdge) return;
	edgeWaypoints.delete(edgeKey(selectedEdge.from, selectedEdge.to));
	const f = activeFileState();
	const cmd = f?.commands.find((c) => c.name === selectedEdge.from);
	if (!cmd) { selectedEdge = null; render(); return; }
	const prereqs = (cmd.header?.prereqs || []).filter((p) => p !== selectedEdge.to);
	const from = selectedEdge.from, to = selectedEdge.to;
	selectedEdge = null;
	await applyOps([{ file: f.path, kind: "setHeader", name: from, header: { prereqs } }], { toast: `removed ${from} < ${to}` });
}

// ---------- lint & status ----------

function renderLint() {
	const bar = $("lintbar");
	bar.innerHTML = "";
	for (const f of state.files) {
		for (const is of f.lint || []) {
			const row = document.createElement("div");
			row.className = "lintrow " + (is.severity === 2 ? "error" : "warning");
			row.innerHTML = `<span class="sev">${is.severity === 2 ? "error" : "warning"}</span>
				<span class="loc">${esc(basename(f.path))}:${is.line}</span>
				<span class="msg">${esc(is.message)}</span>`;
			row.onclick = () => {
				activeFile = f.path;
				const hit = (f.commands || []).find((c) => is.line >= c.doc_start && is.line <= c.end_line);
				if (hit) { sel = { file: f.path, name: hit.name }; edTab = "command"; }
				render();
			};
			bar.appendChild(row);
		}
	}
}

function renderStatus() {
	const errs = [], warns = [];
	for (const f of state.files) {
		if (f.parse_error) errs.push(basename(f.path) + " parse error");
		for (const is of f.lint || []) (is.severity === 2 ? errs : warns).push(is.message);
	}
	const cmds = state.files.reduce((n, f) => n + (f.commands?.length || 0), 0);
	$("statusbar").innerHTML = `
		<span class="s">${state.files.length} file${state.files.length > 1 ? "s" : ""}</span>
		<span class="s">${cmds} command${cmds === 1 ? "" : "s"}</span>
		<span class="s ${state.dirty ? "warn" : "ok"}">${state.dirty ? "● unsaved changes" : "✓ saved"}</span>
		<span class="spacer"></span>
		${errs.length ? `<span class="s err" title="${esc(errs.join("\n"))}">✕ ${errs.length} error${errs.length > 1 ? "s" : ""}</span>` : ""}
		${warns.length ? `<span class="s warn" title="${esc(warns.join("\n"))}">▲ ${warns.length} warning${warns.length > 1 ? "s" : ""}</span>` : ""}
		${!errs.length && !warns.length ? `<span class="s ok">lint clean</span>` : ""}
		<span class="s" style="font-family:var(--mono);font-size:10.5px">Ctrl-S save · Ctrl-Z undo</span>`;

	$("btnSave").classList.toggle("dirty", !!state.dirty);
	$("btnSave").disabled = !state.dirty;
	$("btnUndo").disabled = !state.can_undo;
	$("btnRedo").disabled = !state.can_redo;
}

// ---------- dry run ----------

async function dryRun(targets) {
	modal(`<h3>Dry-run</h3><pre>running…</pre>`);
	const r = await api("/api/dryrun", { targets });
	modal(`
		<h3>Dry-run — ${esc(targets.join(" "))}</h3>
		<pre>${esc(r.output || r.error || "(no output)")}</pre>
		<div class="mrow"><button class="tbtn primary" id="mClose">Close</button></div>`);
	$("mClose").onclick = closeModal;
}

// ---------- add command / import ----------

const TEMPLATES = [
	["{ }", "empty", ""],
	["$ …", "shell", "$ echo hello\n"],
	["go", "go build + test", "$ go build -o app .\n$ go test ./...\n"],
	["docker", "docker build", "$ docker build -t app .\n"],
];

function addCommandModal() {
	const f = activeFileState();
	if (!f) return;
	modal(`
		<h3>New command in ${esc(basename(f.path))}</h3>
		<div class="field"><label>name</label><input type="text" id="ncName" placeholder="build"></div>
		<div class="field"><label>body</label>
			<div class="templategrid">
				${TEMPLATES.map((t, i) => `<button data-t="${i}"><span class="t">${esc(t[0])}</span><span class="d">${esc(t[1])}</span></button>`).join("")}
			</div>
		</div>
		<div class="mrow"><button class="tbtn" id="mCancel">Cancel</button><button class="tbtn primary" id="mOk">Create</button></div>`);
	let tpl = 0;
	document.querySelectorAll(".templategrid button").forEach((b) => {
		b.onclick = () => {
			document.querySelectorAll(".templategrid button").forEach((x) => x.style.borderColor = "");
			b.style.borderColor = "var(--accent)";
			tpl = +b.dataset.t;
		};
	});
	document.querySelector(".templategrid button").style.borderColor = "var(--accent)";
	$("ncName").focus();
	$("mCancel").onclick = closeModal;
	$("mOk").onclick = async () => {
		const name = $("ncName").value.trim();
		if (!name) { $("ncName").focus(); return; }
		closeModal();
		const ok = await applyOps([{
			file: f.path, kind: "insertCommand",
			header: { name },
			body: TEMPLATES[tpl][2],
		}], { toast: "command created" });
		if (ok) { sel = { file: f.path, name }; edTab = "command"; render(); }
	};
}

function addImportModal() {
	const f = activeFileState();
	if (!f) return;
	modal(`
		<h3>Add an import</h3>
		<div class="field"><label>file</label><input type="text" id="impFile" placeholder="lib.constfile"></div>
		<div class="field"><label>namespace (optional)</label><input type="text" id="impNs" placeholder="lib"></div>
		<div class="mrow"><button class="tbtn" id="mCancel">Cancel</button><button class="tbtn primary" id="mOk">Import</button></div>`);
	$("impFile").focus();
	$("mCancel").onclick = closeModal;
	$("mOk").onclick = async () => {
		const file = $("impFile").value.trim();
		const ns = $("impNs").value.trim();
		closeModal();
		if (!file) return;
		const text = `import "${file}"${ns ? " as " + ns : ""}`;
		const ok = await applyOps([{ file: f.path, kind: "insertLines", line: 0, text }], { toast: "import added" });
		if (ok) refresh();
	};
}

boot();
