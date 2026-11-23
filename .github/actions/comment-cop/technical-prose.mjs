// @ts-check

import { findMatches, reasonsFrom } from './matchers.mjs';

/** @typedef {import('./matchers.mjs').Rule} Rule */

const NOUN = '(?:artifacts?|actions?|pins?|versions?|dependenc(?:y|ies)|packages?|images?|names?|aliases|releases?|workflows?|toolchains?|tools?|configurations?|configs?|runtime|states?|updates?|settings?|polic(?:y|ies)|mechanisms?|management|processing|handling|operations?|integrations?|components?|systems?|resources?|services?|behaviors?|support|implementations?|validation)';
const MAINTENANCE_VERB = '(?:align(?:s|ed|ing)?|refresh(?:es|ed|ing)?|sync(?:s|ed|ing)?|synchroniz(?:e|es|ed|ing)|harmoniz(?:e|es|ed|ing)|moderniz(?:e|es|ed|ing)|tid(?:y|ies|ied|ying)|adjust(?:s|ed|ing)?|improv(?:e|es|ed|ing)|enhanc(?:e|es|ed|ing))';
const MAINTENANCE_OBJECT = '(?:toolchains?|tools?|tooling|dependenc(?:y|ies)|packages?|plugins?|configurations?|configs?|settings?|workflows?|environments?|handling|behavior)';

/** @type {Rule[]} */
export const technicalRules = [
	{
		id: 'technical-noun-chain',
		label: 'opaque technical noun chain',
		pattern: new RegExp(`\\b${NOUN}(?:[ \\t]+${NOUN}){2,}\\b`, 'i'),
		guidance: 'Expand the noun chain into a direct description of the object and operation.',
	},
	{
		id: 'temporal-reference',
		label: 'temporal vagueness',
		pattern: /\b(?:current|existing|latest|recent)\b/i,
		guidance: 'Specify the baseline, release, revision, or time period behind the qualifier.',
	},
	{
		id: 'maintenance-claim',
		label: 'vague maintenance claim',
		pattern: new RegExp(`\\b${MAINTENANCE_VERB}[ \\t]+(?:[\\w-]+[ \\t]+){0,5}?${MAINTENANCE_OBJECT}\\b`, 'i'),
		guidance: 'State the concrete change: the version, setting, dependency, or operation.',
	},
	{
		id: 'naming-authority',
		label: 'naming authority',
		pattern: /\b(?:canonical(?:[ \t]+aliases)?|authoritative|primary[ \t]+names?|source[ \t]+of[ \t]+truth)\b/i,
		guidance: 'Describe the actual names and their mapping or precedence.',
	},
	{
		id: 'semicolon-prose',
		label: 'semicolon-joined prose',
		pattern: /(?<!&(?:#[0-9]{1,8}|#x[0-9a-f]{1,8}|[a-z]{1,32}));/i,
		guidance: 'Split the clauses into direct sentences.',
	},
	{
		id: 'stock-wording',
		label: 'stock technical wording',
		pattern: /\b(?:sequenc\w*|retain\w*|suppor\w*|pin(?:s|ned|ning)?|remain\w*|record\w*|cannot|never|named|keep|legacy|distinct\w*|avoid\w*|evidence|independently|does[\s._-]+not)\b/i,
		guidance: 'Describe the specific operation or constraint behind the highlighted wording.',
	},
];

export const technicalGuidance = new Map(technicalRules.map(rule => [rule.label, rule.guidance]));

/** @param {string} text */
export const stockReasons = text => reasonsFrom(findMatches(text, technicalRules.filter(rule => rule.id === 'stock-wording')));

/** @param {string} text */
export const technicalReasons = text => reasonsFrom(findMatches(text, technicalRules.filter(rule => rule.id !== 'stock-wording')));
