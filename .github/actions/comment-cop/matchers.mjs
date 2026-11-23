// @ts-check

/** @typedef {{id: string, label: string, pattern: RegExp, guidance: string}} Rule */
/** @typedef {{ruleId: string, label: string, text: string, start: number, end: number}} WordingMatch */

/** @param {string} text */
function normalize(text) {
	const characters = [];
	const starts = [];
	const ends = [];
	for (const part of text.matchAll(/\s+|\S+/g)) {
		const offset = part.index;
		if (/^\s/.test(part[0])) {
			characters.push(/\n[\t \r]*\n/.test(part[0]) ? '\0' : ' ');
			starts.push(offset);
			ends.push(offset + part[0].length);
		} else {
			for (let i = 0; i < part[0].length; i++) {
				characters.push(part[0][i]);
				starts.push(offset + i);
				ends.push(offset + i + 1);
			}
		}
	}
	return { text: characters.join(''), starts, ends };
}

/** @param {string} text @param {Rule[]} rules @param {string} [searchText] @returns {WordingMatch[]} */
export function findMatches(text, rules, searchText = text) {
	if (searchText.length !== text.length) throw new Error('search text must have the source length');
	const source = normalize(searchText);
	const matches = [];
	for (const rule of rules) {
		const pattern = new RegExp(rule.pattern.source, rule.pattern.flags.replace(/[gy]/g, '') + 'g');
		for (const match of source.text.matchAll(pattern)) {
			if (match[0].length === 0) continue;
			const start = source.starts[match.index];
			const end = source.ends[match.index + match[0].length - 1];
			matches.push({ ruleId: rule.id, label: rule.label, text: text.slice(start, end), start, end });
		}
	}
	return matches;
}

/** @param {WordingMatch[]} matches */
export const reasonsFrom = matches => [...new Set(matches.map(match => match.label))];
