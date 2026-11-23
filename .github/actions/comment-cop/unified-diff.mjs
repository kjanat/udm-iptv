// @ts-check

/** @typedef {{kind: 'removed', path: string} | {kind: 'unpatched', path: string} | {kind: 'patched', path: string, patch: string}} DiffFile */

/** @type {Map<string, number>} */
const ESCAPES = new Map([['a', 7], ['b', 8], ['t', 9], ['n', 10], ['v', 11], ['f', 12], ['r', 13], ['"', 34], ['\\', 92]]);

/** @param {string} text @returns {{name: string, rest: string}} */
function quoted(text) {
	/** @type {number[]} */
	const bytes = [];
	const encoder = new TextEncoder();
	let index = 1;
	while (index < text.length && text[index] !== '"') {
		const character = text[index];
		if (character !== '\\') {
			const codePoint = text.codePointAt(index) ?? 0;
			const symbol = String.fromCodePoint(codePoint);
			bytes.push(...encoder.encode(symbol));
			index += symbol.length;
			continue;
		}
		const octal = /^[0-7]{3}/.exec(text.slice(index + 1));
		if (octal !== null) {
			bytes.push(Number.parseInt(octal[0], 8));
			index += 4;
			continue;
		}
		const escaped = ESCAPES.get(text[index + 1]);
		if (escaped === undefined) throw new Error(`unknown escape in quoted path: ${text}`);
		bytes.push(escaped);
		index += 2;
	}
	if (index >= text.length) throw new Error(`unterminated quoted path: ${text}`);
	return { name: new TextDecoder().decode(new Uint8Array(bytes)), rest: text.slice(index + 1) };
}

/** @param {string} text */
function pathName(text) {
	return text.startsWith('"') ? quoted(text).name : text;
}

/** @param {string} text @returns {string | null} */
function markerPath(text) {
	const name = pathName(text.replace(/\t$/, ''));
	if (name === '/dev/null') return null;
	return name.replace(/^[ab]\//, '');
}

/** @param {string} header */
function headerPath(header) {
	const names = header.slice('diff --git '.length);
	if (names.startsWith('"')) {
		const first = quoted(names);
		const second = first.rest.trimStart();
		return (second.startsWith('"') ? quoted(second).name : second).replace(/^b\//, '');
	}
	const length = (names.length - 5) / 2;
	const name = names.slice(2, 2 + length);
	if (!Number.isInteger(length) || names !== `a/${name} b/${name}`) throw new Error(`ambiguous diff header: ${header}`);
	return name;
}

/** @typedef {{header: string, oldPath: string | null | undefined, newPath: string | null | undefined, renamedTo: string | undefined, deleted: boolean, binary: boolean, hunks: string[] | null}} Section */

/** @param {Section} section @returns {DiffFile} */
function fileFrom(section) {
	const removed = section.deleted || section.newPath === null;
	const path = (removed ? section.oldPath : section.newPath) ?? section.renamedTo ?? headerPath(section.header);
	if (removed) return { kind: 'removed', path };
	if (section.binary) return { kind: 'unpatched', path };
	return { kind: 'patched', path, patch: (section.hunks ?? []).join('\n') };
}

/** @param {string} diff @returns {DiffFile[]} */
export function filesFromDiff(diff) {
	const lines = diff.split('\n');
	if (lines.at(-1) === '') lines.pop();
	/** @type {DiffFile[]} */
	const files = [];
	/** @type {Section | null} */
	let section = null;
	for (const line of lines) {
		if (line.startsWith('diff --git ')) {
			if (section !== null) files.push(fileFrom(section));
			section = { header: line, oldPath: undefined, newPath: undefined, renamedTo: undefined, deleted: false, binary: false, hunks: null };
			continue;
		}
		if (section === null) continue;
		if (section.hunks !== null) {
			section.hunks.push(line);
		} else if (line.startsWith('@@')) {
			section.hunks = [line];
		} else if (line.startsWith('--- ')) {
			section.oldPath = markerPath(line.slice(4));
		} else if (line.startsWith('+++ ')) {
			section.newPath = markerPath(line.slice(4));
		} else if (line.startsWith('rename to ') || line.startsWith('copy to ')) {
			section.renamedTo = pathName(line.slice(line.indexOf(' to ') + 4));
		} else if (line.startsWith('deleted file mode ')) {
			section.deleted = true;
		} else if (line.startsWith('Binary files ') || line === 'GIT binary patch') {
			section.binary = true;
		}
	}
	if (section !== null) files.push(fileFrom(section));
	if (files.length === 0 && diff.trim() !== '') throw new Error('the diff has no file headers');
	return files;
}
