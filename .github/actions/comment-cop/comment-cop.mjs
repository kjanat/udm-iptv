#!/usr/bin/env node
// @ts-check

import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { setTimeout as delay } from 'node:timers/promises';

import { findMatches, reasonsFrom } from './matchers.mjs';
import { technicalGuidance, technicalRules } from './technical-prose.mjs';

/** @typedef {import('./matchers.mjs').Rule} Rule */
/** @typedef {import('./matchers.mjs').WordingMatch} WordingMatch */

/** @typedef {'go' | 'js' | 'hash' | 'md'} Lang */
/** @typedef {{path: string, start: number, end: number, text: string, reasons: string[], matches?: WordingMatch[]}} Group */
/** @typedef {{start: number, end: number, lang: Lang, topLevel: boolean, doc: boolean, lines: string[]}} PendingGroup */
/** @typedef {{id: string, isResolved: boolean, path: string, comments: {nodes: Array<{body: string, viewerDidAuthor: boolean}>}}} ReviewThread */
/** @typedef {{repository: {pullRequest: {reviewThreads: {pageInfo: {hasNextPage: boolean, endCursor: string | null}, nodes: ReviewThread[]}}}}} ReviewThreadsResponse */
/** @typedef {Pick<import('@actions/github-script').AsyncFunctionArguments, 'github' | 'context' | 'core'>} RunArguments */
/** @typedef {{id: number, state: string, body: string | null}} Review */
/** @typedef {{character: '`' | '~', length: number} | null} Fence */

/** @type {Array<[RegExp, Lang]>} */
const LANGS = [
	[/\.go$/, 'go'],
	[/\.(?:mjs|cjs|js|ts|jsonc)$/, 'js'],
	[/\.(?:ya?ml|py|sh|bash|toml)$/, 'hash'],
	[/(?:^|\/)(?:Makefile|Dockerfile)(?:\.[\w.-]+)?$/, 'hash'],
	[/\.md$/, 'md'],
];

const CONTRAST_GUIDANCE =
	'Check whether the comparison explains a real constraint. If it does, keep it; otherwise describe the chosen behavior directly.';
const NON_EFFECT = '(?:un(?:changed|affected|altered|modified|touched|interrupted|disturbed|impacted)|intact|as[- ]is|the same)';
const EFFECT_VERB = '(?:affect|alter|change|touch|modify|interrupt|restart|disrupt|break|disturb|impact)';
const CONTRAST = new RegExp([
	',\\s+not\\s+\\S',
	`\\b(?:remain(?:s|ed|ing)?|stay(?:s|ed|ing)?|is|are|was|were|be|been|being)\\s+(?:(?:otherwise|completely|entirely|fully|still)\\s+)?${NON_EFFECT}\\b`,
	`\\b(?:leave|leaves|leaving|left|keep|keeps|keeping|kept)\\s+(?:[^.;!?\\n]{1,100}?\\s+)?(?:${NON_EFFECT}|alone)\\b`,
	'\\bwithout\\s+(?:affecting|altering|changing|touching|modifying|interrupting|restarting|disrupting|breaking|disturbing|impacting)\\b',
	'\\b(?:without|with no)\\s+(?:any\\s+)?(?:changes?|interruptions?|disruptions?|side[- ]effects?|impact)\\b',
	`\\b(?:(?:does|do|did|will|would|can)\\s+not|doesn't|don't|didn't|won't|wouldn't|can't)\\s+${EFFECT_VERB}\\b`,
	`\\bcontinu(?:e|es|ed|ing)\\s+(?:to\\s+(?:run|work|operate)\\s+)?(?:${NON_EFFECT}|normally|as before|as usual)\\b`,
].join('|'), 'i');
const VERIFICATION_DISCLAIMER = new RegExp([
	'\\b(?:unverified|untested|unvalidated|unconfirmed)\\b',
	"\\b(?:not|never|hasn't|haven't|wasn't|weren't|isn't|aren't)\\s+(?:(?:yet|been|independently|locally|fully|manually|actually|explicitly|directly|end[- ]to[- ]end)\\s+){0,4}(?:verified|tested|validated|confirmed|checked|proven|established)\\b",
	"\\b(?:cannot|can't|could not|couldn't|unable to)\\s+(?:(?:be|independently|locally|fully|directly)\\s+){0,3}(?:confirm(?:ed)?|verif(?:y|ied)|validat(?:e|ed)|prov(?:e|en)|establish(?:ed)?)\\b",
	'\\b(?:does not|doesn\'t|do not|don\'t)\\s+(?:prove|confirm|verify|establish)\\b',
	'\\b(?:verification|validation|testing|tests?|checks?)\\s+(?:(?:is|are|was|were|has|have)\\s+)?(?:not\\s+(?:been\\s+)?(?:performed|run|done)|pending)\\b',
].join('|'), 'i');

const DETERMINER = '(?:the|a|an|its|their|this|that|these|those|each|every|any)';
const INNER_DETERMINER = '(?:the|a|an|its|their|this|these|those|each|every|any)';
const AUXILIARY = '(?:is|are|was|were|be|been|being|has|have|had|will|can|could|should|would|may|might|must|do|does|did|not|and|or)';
const ING_ADJECTIVE =
	'(?!(?:following|remaining|existing|missing|pending|underlying|corresponding|resulting|during|string|setting|thing|something|nothing|anything|everything|warning|morning|evening|building|meaning|beginning|ending|listing|according)\\b)';
const OBJECT_OR_PREPOSITION = '(?:it|them|its|their|the|a|an|this|that|these|those|to|from|with|on|in|at|by|for|under|behind|over)';
const POST_MODIFIER = new RegExp(
	`\\b${DETERMINER}\\s+\\w+\\s+${INNER_DETERMINER}\\s+\\w+\\s+\\w+(?:s|ed)\\b`
		+ `|\\b${DETERMINER}\\s+\\w+\\s+that\\s+(?!\\w+s\\b)\\w+\\s+\\w+(?:s|ed)\\b`
		+ `|\\b${DETERMINER}\\s+(?:(?!${AUXILIARY}\\b)\\w+\\s+){1,2}?${ING_ADJECTIVE}\\w+ing\\s+${OBJECT_OR_PREPOSITION}\\b`
		+ `|\\b${DETERMINER}\\s+(?:(?!${AUXILIARY}\\b)\\w+\\s+){1,3}?\\w+ed\\s+by\\b`,
	'i',
);

/** @type {Array<[string, RegExp, string]>} */
const TELLS = [
	[
		'em dash',
		/[—–]/,
		'For ordinary prose, consider a comma, colon, parentheses, or separate sentence. Preserve punctuation that is part of quoted material.',
	],
	['"X, not Y"', CONTRAST, CONTRAST_GUIDANCE],
	['"X rather than Y"', /\brather than\b/i, CONTRAST_GUIDANCE],
	['"X instead of Y"', /\b(?:instead of|as opposed to)\b/i, CONTRAST_GUIDANCE],
	['"X, no longer Y"', /\b(?:no longer|in place of|and not)\b|(?<!\b(?:leaves?|left)\b(?:\s+\w+){0,8}\s)\balone\b/i, CONTRAST_GUIDANCE],
	[
		'narrated history',
		/(?:^|[.;:]\s+)(?:before|previously|until now|so far),\s|\bused to\b/im,
		'Describe the behavior as it is now. The diff already shows what it replaced.',
	],
	['"not just X but Y"', /\bnot (?:just|merely|only|because)\b[^.]{0,80}?\bbut\b/i, CONTRAST_GUIDANCE],
	[
		'emphatic cleft',
		/\b(?:which|that) is (?:what|why|how)\b|\bexactly (?:what|why|how|the)\b/i,
		'Consider stating the behavior or reason directly. Keep the emphasis if it carries a meaningful distinction.',
	],
	[
		'filler phrase',
		/\b(?:in other words|it(?:'s| is) (?:worth noting|important to note)|that said|under the hood|at its core|(?:simply put|put simply)|in short|in essence|bottom line|needless to say|when it comes to|at the end of the day|think of (?:it|this) as|no more,? no less|(?:that|which) is to say|here(?:'s| is) (?:why|the thing)|the (?:whole|entire) point|the key (?:insight|takeaway))\b/i,
		'Check whether the introductory phrase adds meaning. If the explanation reads clearly without it, omit the phrase.',
	],
	[
		'inflated diction',
		/\b(?:leverag(?:e|es|ing)|utiliz(?:e|es|ing)|seamless(?:ly)?|delv(?:e|es|ing)|myriad|plethora|robust|comprehensive(?:ly)?|crucial(?:ly)?|vital(?:ly)?|elegant(?:ly)?|powerful(?:ly)?|intuitive(?:ly)?|nuanced|holistic|granular|meticulous|facilitat(?:e|es|ing)|streamlin(?:e|es|ing)|empower(?:s|ing)?|cutting[-\s]edge|state[-\s]of[-\s]the[-\s]art|arguably|essentially|fundamentally|a wealth of)\b/i,
		'Consider a plain, precise term. Keep the existing word if it has a specific technical meaning here.',
	],
	[
		'connective glue',
		/\b(?:moreover|furthermore|conversely|as such|it turns out|notably|importantly)\b/i,
		'Check whether the transition helps connect the surrounding points. It can be omitted when that connection is already clear.',
	],
	[
		'counterfactual justification',
		/\bso\b[^.]{0,60}\b(?:cannot|can't|could not|never|would)\b|\bwithout\b[^.]{0,70}\bwould\b|\bwould otherwise\b|\botherwise\b[^.]{0,70}\bwould\b|\bso that\b|\b(?:which|that) (?:prevents|keeps|stops)\b/i,
		'An explanation of a consequence or failure mode can be useful. Keep it when it documents a non-obvious constraint; otherwise state the behavior directly.',
	],
	[
		'verification disclaimer',
		VERIFICATION_DISCLAIMER,
		'Describe the implemented check and its scope. If a verification limit matters, identify the unchecked behavior and the check needed to resolve it.',
	],
	[
		'hedge',
		/\b(?:not (?:yet )?verified|unverified|may (?:be|have|not|still|already)|might|possibly|probably|likely|appears? to|seems? to|cannot (?:be )?(?:confirm|verif|prov|establish)\w*|does not prove|remains? unknown|not (?:been )?(?:checked|proven|confirmed|established))\b/i,
		'State the fact the code establishes. A qualifier such as "may" or "not verified" leaves the reader to guess what was checked.',
	],
	[
		'participial post-modifier',
		POST_MODIFIER,
		'Write a possessive or a plain relative clause: "its origin", "the marker that the daemon writes".',
	],
	[
		'paste artifact',
		/[“”‘’]|[\u00A0\u00AD\u200B-\u200D\uFEFF]/,
		'Check typographic quotes and nonstandard whitespace for accidental pasted characters. Preserve intentional examples and quotations.',
	],
];

const BASELINE_IDS = new Map([
	['em dash', 'em-dash'],
	['"X, not Y"', 'contrast-not'],
	['"X rather than Y"', 'contrast-rather-than'],
	['"X instead of Y"', 'contrast-instead-of'],
	['"X, no longer Y"', 'contrast-no-longer'],
	['narrated history', 'narrated-history'],
	['"not just X but Y"', 'contrast-not-just'],
	['emphatic cleft', 'emphatic-cleft'],
	['filler phrase', 'filler-phrase'],
	['inflated diction', 'inflated-diction'],
	['connective glue', 'connective-glue'],
	['counterfactual justification', 'counterfactual-justification'],
	['verification disclaimer', 'verification-disclaimer'],
	['hedge', 'hedge'],
	['participial post-modifier', 'participial-post-modifier'],
	['paste artifact', 'paste-artifact'],
]);

/** @type {Rule[]} */
export const wordingRules = [
	...TELLS.map(([label, pattern, guidance]) => ({ id: BASELINE_IDS.get(label) ?? label, label, pattern, guidance })),
	...technicalRules,
];

/** @param {string} text @param {string} [searchText] */
export const wordingMatches = (text, searchText) => findMatches(text, wordingRules, searchText);

const TOP_LEVEL_DECL = /^(?:package|const|func|type|var)\b/;
const GO_STRING = /"(?:[^"\\\n]|\\.)*"|`[^`]*`/g;
const GO_STRUCT_TAG = /^`\w+:"/;
const MD_ITEM = /^\s*(?:[-*+]\s|\d+[.)]\s|#{1,6}\s|\||>\s)/;

/** @param {string} path @returns {Lang | null} */
function langFor(path) {
	for (const [pattern, lang] of LANGS) {
		if (pattern.test(path)) return lang;
	}
	return null;
}

/** @param {string} line @param {Lang} lang */
function isCommentLine(line, lang) {
	const text = line.trimStart();
	if (lang === 'hash') return text.startsWith('#') && !text.startsWith('#!');
	return text.startsWith('//')
		|| text.startsWith('/*')
		|| text === '*'
		|| text === '*/'
		|| text.startsWith('* ');
}

/** @param {string} line */
function stripCommentPrefix(line) {
	return line
		.trimStart()
		.replace(/^\/\/\s?/, '')
		.replace(/^\/\*\*?\s?/, '')
		.replace(/^#\s?/, '')
		.replace(/^\*\s?/, '')
		.trim();
}

/** @param {PendingGroup} group @param {string} nextLine @param {string[] | undefined} sourceLines */
function isDocBlock(group, nextLine, sourceLines) {
	if (group.doc) return true;
	if (group.lang !== 'go') return false;
	let firstLine = group.lines[0];
	if (sourceLines !== undefined) {
		let before = group.start - 1;
		let after = group.end;
		while (before > 0 && isCommentLine(sourceLines[before - 1], 'go')) before--;
		while (after < sourceLines.length && isCommentLine(sourceLines[after], 'go')) after++;
		firstLine = sourceLines[before] ?? firstLine;
		nextLine = sourceLines[after] ?? nextLine;
	}
	if (group.topLevel && TOP_LEVEL_DECL.test(nextLine)) return true;
	const field = /^\s+([A-Za-z_]\w*)\s+(?:[A-Za-z_*]|\[)/.exec(nextLine)?.[1];
	return field !== undefined && stripCommentPrefix(firstLine).startsWith(`${field} `);
}

/** @typedef {{kind: 'commit' | 'description', label: string, text: string, reasons: string[], matches?: WordingMatch[]}} ProseFinding */

/** @param {string} label @param {string} text @returns {ProseFinding[]} */
export function proseFindings(label, text) {
	const matches = wordingMatches(text);
	const reasons = reasonsFrom(matches);
	return reasons.length === 0 ? [] : [{ kind: 'description', label, text, reasons, matches }];
}

const SUBJECT_LIMIT = 50;
const LONG_SUBJECT = `subject longer than ${SUBJECT_LIMIT} characters`;

/** @param {string} label @param {string} message @returns {ProseFinding[]} */
export function commitFindings(label, message) {
	const matches = wordingMatches(message);
	const subject = message.split('\n', 1)[0];
	if (subject.length > SUBJECT_LIMIT) matches.unshift({ ruleId: 'subject-length', label: LONG_SUBJECT, text: subject, start: 0, end: subject.length });
	const reasons = reasonsFrom(matches);
	return reasons.length === 0 ? [] : [{ kind: 'commit', label, text: message, reasons, matches }];
}

/** @param {PendingGroup} group @param {string} nextLine @param {string[] | undefined} sourceLines */
function detailsFor(group, nextLine, sourceLines) {
	const text = group.lines.join('\n');
	const searchText = group.lang === 'md' ? text : group.lines.map(line => {
		const prefix = /^\s*(?:\/\/|\/\*\*?|#|\*)\s?/.exec(line)?.[0] ?? '';
		return ' '.repeat(prefix.length) + line.slice(prefix.length);
	}).join('\n');
	const matches = wordingMatches(text, searchText);
	if (group.lang !== 'md' && group.lines.length >= 3 && !isDocBlock(group, nextLine, sourceLines)) {
		matches.unshift({ ruleId: 'comment-length', label: `${group.lines.length} lines`, text, start: 0, end: text.length });
	}
	return { reasons: reasonsFrom(matches), matches };
}

/** @param {string} line */
function goStringTexts(line) {
	const literals = [];
	for (const match of line.matchAll(GO_STRING)) {
		if (GO_STRUCT_TAG.test(match[0])) continue;
		literals.push({ text: match[0].slice(1, -1), offset: match.index + 1 });
	}
	return literals;
}

/** @param {Fence} fence @param {string} line @returns {Fence} */
function updateFence(fence, line) {
	const match = /^ {0,3}(`{3,}|~{3,})(.*)$/.exec(line);
	if (match === null) return fence;
	const run = match[1];
	const character = run[0];
	if (character !== '`' && character !== '~') return fence;
	if (fence === null) return { character, length: run.length };
	if (character === fence.character && run.length >= fence.length && match[2].trim() === '') return null;
	return fence;
}

/** @param {string} source @param {number} line */
function fenceBefore(source, line) {
	/** @type {Fence} */
	let fence = null;
	for (const content of source.split('\n').slice(0, Math.max(0, line - 1))) {
		fence = updateFence(fence, content);
	}
	return fence;
}

/** @param {string} path @param {string} patch @param {string} [source] @returns {Group[]} */
export function groupsFromPatch(path, patch, source) {
	const lang = langFor(path);
	if (lang === null) return [];
	const markdown = lang === 'md';
	const sourceLines = source?.split('\n');
	/** @type {Group[]} */
	const groups = [];
	/** @type {PendingGroup | null} */
	let pending = null;
	let newLine = 0;
	/** @type {Fence} */
	let fence = null;

	/** @param {string} nextLine */
	const flush = nextLine => {
		if (pending !== null) {
			const { reasons, matches } = detailsFor(pending, nextLine, sourceLines);
			if (reasons.length > 0) {
				groups.push({
					path,
					start: pending.start,
					end: pending.end,
					text: pending.lines.join('\n'),
					reasons,
					matches,
				});
			}
		}
		pending = null;
	};

	/** @param {string} content */
	const startsBlock = content => {
		if (!markdown) return isCommentLine(content, lang);
		const nextFence = updateFence(fence, content);
		if (nextFence !== fence) {
			fence = nextFence;
			return false;
		}
		return fence === null && content.trim() !== '' && !/^(?: {4}|\t)/.test(content);
	};

	for (const raw of patch.split('\n')) {
		if (raw.startsWith('@@')) {
			flush('');
			const match = /\+(\d+)/.exec(raw);
			newLine = match === null ? 1 : Number.parseInt(match[1], 10);
			fence = markdown && source !== undefined ? fenceBefore(source, newLine) : null;
			continue;
		}

		if (raw.startsWith('+')) {
			const content = raw.slice(1);
			if (startsBlock(content)) {
				const text = content.trimStart();
				if (pending !== null && (markdown ? MD_ITEM.test(content) : text.startsWith('/**'))) {
					flush(content);
				}
				if (pending === null) {
					pending = {
						start: newLine,
						end: newLine,
						lang,
						topLevel: !/^[\t ]/.test(content),
						doc: text.startsWith('/**'),
						lines: [content],
					};
				} else {
					pending.end = newLine;
					pending.lines.push(content);
				}
			} else {
				flush(content);
				if (lang === 'go') {
					const matches = goStringTexts(content).flatMap(literal => wordingMatches(literal.text).map(match => ({
						...match, start: match.start + literal.offset, end: match.end + literal.offset,
					})));
					const reasons = reasonsFrom(matches);
					if (reasons.length > 0) {
						groups.push({ path, start: newLine, end: newLine, text: content, reasons, matches });
					}
				}
			}
			newLine++;
			continue;
		}

		if (raw.startsWith('-')) {
			flush('');
			continue;
		}
		if (raw.startsWith('\\')) continue;

		const content = raw.startsWith(' ') ? raw.slice(1) : raw;
		if (markdown) fence = updateFence(fence, content);
		flush(content);
		newLine++;
	}

	flush('');
	return groups;
}

/** @param {Group} group */
export const keyFor = group =>
	createHash('sha256')
		.update(group.path)
		.update('\0')
		.update(String(group.start))
		.update('\0')
		.update(group.text)
		.digest('hex')
		.slice(0, 16);

/** @type {'RIGHT'} */
const RIGHT = 'RIGHT';

/** @param {string} reason */
function guidanceFor(reason) {
	if (/^\d+ lines$/.test(reason)) {
		return 'This is a length-only flag. Check whether each line adds useful context; a necessary explanation can stay.';
	}
	if (reason === LONG_SUBJECT) {
		return `Keep the first line of a commit message to ${SUBJECT_LIMIT} characters and put the rest in the body.`;
	}
	return technicalGuidance.get(reason) ?? TELLS.find(([name]) => name === reason)?.[2] ?? 'Review the flagged wording in context.';
}

/** @param {WordingMatch[] | undefined} matches */
function matchDetails(matches) {
	if (matches === undefined || matches.length === 0) return '';
	const entries = matches.map(match => {
		const text = JSON.stringify(match.text).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
		return `- \`${match.ruleId}\`: <code>${text}</code> [${match.start}, ${match.end})`;
	});
	return `\n\nMatches:\n\n${entries.join('\n')}`;
}

/** @param {Group} group */
export function bodyFor(group) {
	const guidance = [...new Set(group.reasons.map(guidanceFor))];
	const advice = guidance.length === 1 ? guidance[0] : guidance.map(text => `- ${text}`).join('\n');
	return `<!-- actionlint-comment-cop:${keyFor(group)} -->\n`
		+ `Flagged for: ${group.reasons.join(', ')}.\n\n${advice}${matchDetails(group.matches)}\n\n`
		+ `<sub>Comment Cop is intentionally sensitive; its suggestions are advisory. `
		+ `If this is a false positive, you are welcome to resolve this thread without changing the text.</sub>`
		+ `<br><sub>Any AI agents are only allowed to resolve the comment if it is a false positive and need to justify the closure with a reply. Otherwise fix it. Comment-cop will resolve it automatically on next push.</sub>`;
}

/** @param {unknown} error */
function errorMessage(error) {
	return error instanceof Error ? error.message : String(error);
}

const PROSE_MARKER = '<!-- actionlint-comment-cop:prose -->';

/** @param {string} text */
const quote = text => text.split('\n').map(line => `> ${line}`).join('\n');

/** @param {ProseFinding} finding */
function proseQuote(finding) {
	if (finding.kind === 'description') {
		return `<details>\n<summary>description</summary>\n\n${quote(finding.text.trim())}\n</details>`;
	}
	const [subject, ...rest] = finding.text.trim().split('\n');
	const body = rest.join('\n').trim();
	if (body === '') return quote(subject);
	return `${quote(subject)}\n\n<details>\n<summary>body</summary>\n\n${quote(body)}\n</details>`;
}

/** @param {ProseFinding[]} findings */
export function proseBody(findings) {
	const sections = findings.map(finding => {
		const guidance = [...new Set(finding.reasons.map(guidanceFor))].join('\n');
		return `${finding.label}, flagged for: ${finding.reasons.join(', ')}.\n\n${proseQuote(finding)}\n\n${guidance}${matchDetails(finding.matches)}`;
	});
	return `${PROSE_MARKER}\nComment Cop found this wording in the commit messages or the description. `
		+ `Reword the commits and push again; this review clears itself on the next run.\n\n${sections.join('\n\n')}`;
}

const INLINE_BODY = 'Please review the flagged wording in the inline comments.';
const SECONDARY_LIMIT = /secondary rate limit/i;
const DEFAULT_RETRY_SECONDS = 60;
const COMMENT_BATCH = 20;
const BATCH_PAUSE_MS = 20_000;
const RUN_COMMENT_LIMIT = 400;

/** @param {unknown} error */
function retryAfterSeconds(error) {
	if (typeof error !== 'object' || error === null) return undefined;
	const status = 'status' in error ? error.status : undefined;
	if (status !== 403 && status !== 429) return undefined;
	const response = 'response' in error ? error.response : undefined;
	const headers = typeof response === 'object' && response !== null && 'headers' in response ? response.headers : undefined;
	const header = typeof headers === 'object' && headers !== null && 'retry-after' in headers ? headers['retry-after'] : undefined;
	const seconds = Number.parseInt(String(header), 10);
	if (Number.isFinite(seconds) && seconds > 0) return seconds;
	return SECONDARY_LIMIT.test(errorMessage(error)) ? DEFAULT_RETRY_SECONDS : undefined;
}

/** @typedef {Parameters<RunArguments['github']['rest']['pulls']['createReview']>[0]} ReviewParams */
/** @typedef {(ms: number) => Promise<unknown>} Sleep */

/** @param {Pick<RunArguments, 'github' | 'core'>} args @param {ReviewParams} params @param {Sleep} sleep */
async function submitReview({ github, core }, params, sleep) {
	try {
		await github.rest.pulls.createReview(params);
		return true;
	} catch (error) {
		const seconds = retryAfterSeconds(error);
		if (seconds === undefined) {
			core.setFailed(`Could not submit Comment Cop review: ${errorMessage(error)}`);
			return false;
		}
		core.warning(`GitHub asked Comment Cop to wait ${seconds}s before posting its review.`);
		await sleep(seconds * 1000);
	}
	try {
		await github.rest.pulls.createReview(params);
		return true;
	} catch (error) {
		core.setFailed(`Could not submit Comment Cop review after waiting: ${errorMessage(error)}`);
		return false;
	}
}

/** @param {unknown} value @param {string} name */
function requiredString(value, name) {
	if (typeof value !== 'string' || value === '') throw new Error(`${name} is required`);
	return value;
}

/** @param {RunArguments} args @param {Sleep} [sleep] */
export default async function run({ github, context, core }, sleep = delay) {
	const pullRequest = context.payload.pull_request;
	if (pullRequest === undefined) throw new Error('pull_request payload is required');

	const owner = context.repo.owner;
	const repo = context.repo.repo;
	const pullNumber = pullRequest.number;
	const headSha = requiredString(pullRequest.head.sha, 'pull request head SHA');
	const files = await github.paginate(github.rest.pulls.listFiles, {
		owner,
		repo,
		pull_number: pullNumber,
		per_page: 100,
	});

	/** @type {Group[]} */
	const groups = [];
	const unscannedPaths = new Set();
	for (const file of files) {
		if (file.status === 'removed' || langFor(file.filename) === null) continue;
		if (file.filename.startsWith('vendor/') || file.filename.includes('/vendor/')) continue;
		if (file.patch === undefined) {
			unscannedPaths.add(file.filename);
			core.warning(`No patch available for ${file.filename}; skipping comment scan.`);
			continue;
		}

		let source;
		if (file.filename.endsWith('.go') || langFor(file.filename) === 'md') {
			try {
				const contentsUrl = requiredString(file.contents_url, `contents URL for ${file.filename}`);
				const response = await github.request(contentsUrl, {
					headers: { accept: 'application/vnd.github.raw+json' },
				});
				source = requiredString(response.data, `contents of ${file.filename}`);
			} catch (error) {
				unscannedPaths.add(file.filename);
				core.warning(`Could not read ${file.filename}; skipping comment scan: ${errorMessage(error)}`);
				continue;
			}
		}
		groups.push(...groupsFromPatch(file.filename, file.patch, source));
	}

	const commits = await github.paginate(github.rest.pulls.listCommits, {
		owner,
		repo,
		pull_number: pullNumber,
		per_page: 100,
	});
	const prose = [
		...commits.flatMap(commit =>
			commitFindings(`Commit ${context.serverUrl}/${owner}/${repo}/commit/${commit.sha}`, commit.commit.message)
		),
		...proseFindings('pull request description', pullRequest.body ?? ''),
	];
	const reviews = await github.paginate(github.rest.pulls.listReviews, {
		owner,
		repo,
		pull_number: pullNumber,
		per_page: 100,
	});
	const openProse = reviews.filter(review =>
		review.state === 'CHANGES_REQUESTED' && typeof review.body === 'string' && review.body.startsWith(PROSE_MARKER)
	);

	const presentKeys = new Set(groups.map(keyFor));
	const seenKeys = new Set();
	/** @type {string[]} */
	const staleThreadIds = [];
	let after = null;
	for (;;) {
		/** @type {ReviewThreadsResponse} */
		const response = await github.graphql(
			`query($owner: String!, $repo: String!, $pr: Int!, $after: String) {
				repository(owner: $owner, name: $repo) {
					pullRequest(number: $pr) {
						reviewThreads(first: 100, after: $after) {
							pageInfo { hasNextPage endCursor }
							nodes { id isResolved path comments(first: 1) { nodes { body viewerDidAuthor } } }
						}
					}
				}
			}`,
			{ owner, repo, pr: pullNumber, after },
		);
		const page = response.repository.pullRequest.reviewThreads;
		for (const thread of page.nodes) {
			const comment = thread.comments.nodes[0];
			if (comment === undefined || !comment.viewerDidAuthor) continue;
			const match = /<!-- actionlint-comment-cop:([a-f0-9]+) -->/.exec(comment.body);
			if (match === null) continue;
			const key = match[1];
			seenKeys.add(key);
			if (!thread.isResolved && !presentKeys.has(key) && !unscannedPaths.has(thread.path)) {
				staleThreadIds.push(thread.id);
			}
		}
		if (!page.pageInfo.hasNextPage) break;
		after = page.pageInfo.endCursor;
	}

	let resolved = 0;
	for (const id of staleThreadIds) {
		try {
			await github.graphql(
				`mutation($id: ID!) {
					resolveReviewThread(input: {threadId: $id}) { thread { id } }
				}`,
				{ id },
			);
			resolved++;
		} catch (error) {
			core.warning(`Could not resolve Comment Cop thread ${id}: ${errorMessage(error)}`);
		}
	}

	const comments = groups.filter(group => !seenKeys.has(keyFor(group))).map(group => ({
		path: group.path,
		line: group.end,
		side: RIGHT,
		body: bodyFor(group),
		...(group.start < group.end
			? { start_line: group.start, start_side: RIGHT }
			: {}),
	}));
	/** @type {Array<typeof comments | undefined>} */
	const batches = [];
	for (let start = 0; start < Math.min(comments.length, RUN_COMMENT_LIMIT); start += COMMENT_BATCH) {
		batches.push(comments.slice(start, start + COMMENT_BATCH));
	}
	const body = prose.length > 0 ? proseBody(prose) : INLINE_BODY;
	const current = prose.length > 0 && comments.length === 0 ? openProse.find(review => review.body === body) : undefined;
	if (batches.length === 0 && prose.length > 0 && current === undefined) batches.push(undefined);
	let posted = 0;
	let firstSubmitted = batches.length === 0;
	for (let index = 0; index < batches.length; index++) {
		if (index > 0) await sleep(BATCH_PAUSE_MS);
		const batch = batches[index];
		const submitted = await submitReview({ github, core }, {
			owner,
			repo,
			pull_number: pullNumber,
			commit_id: headSha,
			event: index === 0 && prose.length > 0 ? 'REQUEST_CHANGES' : 'COMMENT',
			body: index === 0 ? body : `Comment Cop, part ${index + 1} of ${batches.length}.`,
			...(batch === undefined ? {} : { comments: batch }),
		}, sleep);
		if (!submitted) break;
		if (index === 0) firstSubmitted = true;
		posted += batch?.length ?? 0;
	}
	const keepOpen = prose.length > 0 && !firstSubmitted;
	for (const review of openProse) {
		if (review === current || keepOpen) continue;
		try {
			await github.rest.pulls.dismissReview({
				owner,
				repo,
				pull_number: pullNumber,
				review_id: review.id,
				message: prose.length === 0 ? 'Comment Cop: the commit messages and the description are clean.' : 'Comment Cop: superseded.',
			});
		} catch (error) {
			core.warning(`Could not dismiss Comment Cop review ${review.id}: ${errorMessage(error)}`);
		}
	}
	if (prose.length > 0) {
		core.setFailed(`Comment Cop: ${prose.length} finding(s) in the commit messages or the description.`);
	}

	core.info(
		`Comment Cop: ${posted} posted, ${comments.length - posted} left for the next run, ${resolved} stale threads resolved, ${groups.length} present, ${prose.length} in commit messages and the description.`,
	);
}

const git = (/** @type {string[]} */ args) => execFileSync('git', args, { encoding: 'utf8', maxBuffer: 1 << 28 });

function defaultBase() {
	for (const ref of ['origin/master', 'master']) {
		try {
			return execFileSync('git', ['merge-base', 'HEAD', ref], {
				encoding: 'utf8',
				stdio: ['ignore', 'pipe', 'pipe'],
			}).trim();
		} catch {
			// Ignore errors
		}
	}
	throw new Error('no origin/master or master found; pass a base ref');
}

/** @param {string} name */
function untrackedPatch(name) {
	try {
		return git(['diff', '--no-index', '--', '/dev/null', name]);
	} catch (error) {
		if (error instanceof Error && 'stdout' in error && typeof error.stdout === 'string') {
			return error.stdout;
		}
		return '';
	}
}

/** @param {string} output */
const outputLines = output => output.split('\n').map(line => line.trim()).filter(Boolean);

/** @param {string} name */
const scannable = name => langFor(name) !== null && !name.startsWith('vendor/') && !name.includes('/vendor/');

/** @param {string | undefined} base @param {{json?: boolean}} [options] */
export function scanLocal(base, options = {}) {
	const from = base ?? defaultBase();
	const tracked = outputLines(git(['diff', '--name-only', '--diff-filter=d', from, '--'])).filter(scannable);
	const untracked = outputLines(git(['ls-files', '--others', '--exclude-standard'])).filter(scannable);
	const groups = [
		...tracked.flatMap(name =>
			groupsFromPatch(name, git(['diff', '-U3', from, '--', name]), readFileSync(name, 'utf8'))
		),
		...untracked.flatMap(name => groupsFromPatch(name, untrackedPatch(name), readFileSync(name, 'utf8'))),
	];

	const prose = localCommitFindings(from);
	const total = groups.length + prose.length;
	if (options.json) {
		console.log(JSON.stringify({ base: from, total, groups, prose }, null, 2));
	} else {
		for (const group of groups) {
			console.log(`${group.path}:${group.start}-${group.end} [${group.reasons.join(', ')}]`);
			console.log(`${group.text}\n`);
			printMatches(group.matches);
		}
		for (const finding of prose) {
			console.log(`${finding.label} [${finding.reasons.join(', ')}]`);
			console.log(`${finding.text.trim()}\n`);
			printMatches(finding.matches);
		}
		console.log(`comment-cop: ${total === 0 ? 'clean' : `${total} finding(s)`}.`);
	}
	return total === 0 ? 0 : 1;
}

/** @param {WordingMatch[] | undefined} matches */
function printMatches(matches) {
	for (const match of matches ?? []) {
		console.log(`  ${match.ruleId}: ${JSON.stringify(match.text)} [${match.start}, ${match.end})`);
	}
}

export function ruleCatalog() {
	return [
		...wordingRules.map(({ id, label, pattern, guidance }) => ({ id, label, pattern: pattern.toString(), guidance })),
		{ id: 'subject-length', label: LONG_SUBJECT, guidance: guidanceFor(LONG_SUBJECT), limit: SUBJECT_LIMIT },
		{ id: 'comment-length', label: 'implementation comment length', guidance: guidanceFor('3 lines'), limit: 3 },
	];
}

/** @param {string} from */
function localCommitFindings(from) {
	const parts = git(['log', '--format=%H%x00%B%x00', `${from}..HEAD`]).split('\0');
	/** @type {ProseFinding[]} */
	const findings = [];
	for (let index = 0; index + 1 < parts.length; index += 2) {
		const sha = parts[index].trim();
		if (sha === '') continue;
		findings.push(...commitFindings(`commit ${sha.slice(0, 7)}`, parts[index + 1]));
	}
	return findings;
}

if (import.meta.main) {
	const args = process.argv.slice(2);
	const positional = args.filter(arg => !arg.startsWith('-'));
	const invalid = args.some(arg => arg.startsWith('-') && !['--json', '--rules', '--help', '-h'].includes(arg))
		|| positional.length > 1 || (args.includes('--rules') && positional.length > 0);
	if (invalid || args.includes('--help') || args.includes('-h')) {
		console.log('usage: comment-cop.mjs [--json] [<base-ref>]\n       comment-cop.mjs --rules\n\n--json   Print findings with rule IDs, matching text and offsets.\n--rules  Print the rule catalog and guidance.\n\nSuppressions: none. Every wording match is reported.\n\nSource scan: comments, Markdown prose and Go strings. Vendor paths, unsupported file types, Markdown code fences and Go struct tags are outside the source scan.\nCommit messages and PR descriptions are scanned in full.\nOffsets: zero-based UTF-16 positions within each finding text, with an exclusive end.');
		process.exitCode = invalid ? 2 : 0;
	} else if (args.includes('--rules')) {
		console.log(JSON.stringify(ruleCatalog(), null, 2));
	} else {
		try {
			process.exitCode = scanLocal(positional[0], { json: args.includes('--json') });
		} catch (error) {
			console.error(errorMessage(error));
			process.exitCode = 2;
		}
	}
}
