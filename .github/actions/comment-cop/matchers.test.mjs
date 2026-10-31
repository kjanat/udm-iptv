import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import test from 'node:test';

import { findMatches } from './matchers.mjs';
import { bodyFor, commitFindings, groupsFromPatch, proseBody, ruleCatalog, wordingMatches } from './comment-cop.mjs';

test('covers every baseline wording family with its stable rule ID', () => {
	for (const [ruleId, text] of [
		['em-dash', 'Read the file — apply the values.'],
		['contrast-not', 'Describe the setting, not the parser.'],
		['contrast-rather-than', 'Use the cache rather than fetching twice.'],
		['contrast-instead-of', 'Use the cache instead of fetching twice.'],
		['contrast-no-longer', 'It no longer retries.'],
		['narrated-history', 'Previously, it retried.'],
		['contrast-not-just', 'Not just a parser but a renderer.'],
		['emphatic-cleft', 'That is why it retries.'],
		['filler-phrase', 'That said, parse the file.'],
		['inflated-diction', 'Robust parsing.'],
		['connective-glue', 'Moreover, parse the file.'],
		['counterfactual-justification', 'Parse the file so that the values are available.'],
		['hedge', 'It might retry.'],
		['hedge', 'Not verified.'],
		['hedge', 'Unverified.'],
		['hedge', 'Cannot provide the value.'],
		['hedge', 'Cannot be verifying the value.'],
		['hedge', 'Does not prove delivery.'],
		['hedge', 'Not been checked.'],
		['hedge', 'Not been proven.'],
		['hedge', 'Not been confirmed.'],
		['hedge', 'Not been established.'],
		['participial-post-modifier', 'The marker the daemon writes is owned state.'],
		['paste-artifact', 'The “setting” is a flag.'],
	]) {
		const matches = wordingMatches(text);
		assert.equal(matches.some(m => m.ruleId === ruleId), true, ruleId + ': ' + text);
	}
	const subject = 'x'.repeat(51);
	assert.equal(commitFindings('commit', subject)[0].matches[0].ruleId, 'subject-length');
	const [group] = groupsFromPatch('rule.go', '@@ -0,0 +1,4 @@\n+\t// Parse input.\n+\t// Apply values.\n+\t// Return output.\n+\tparse()\n');
	assert.equal(group.matches.some(m => m.ruleId === 'comment-length'), true);
});

test('reports exact source slices after wrapping, indentation and Unicode', () => {
	const text = '😀 Keep\n\tlegacy values.\n\nNot\r\n   independently verified. Keep both.';
	const matches = wordingMatches(text);
	for (const match of matches) {
		assert.equal(text.slice(match.start, match.end), match.text);
	}
	assert.deepEqual(matches.filter(m => m.ruleId === 'stock-wording').map(m => m.text), ['Keep', 'legacy', 'independently', 'Keep']);
	assert.equal(matches.some(m => m.ruleId === 'verification-disclaimer' && m.text === 'Not\r\n   independently verified'), true);
});

test('maps comment and literal matches to their original text block', () => {
	for (const [path, source] of [
		['rule.go', '\t// Current artifact\n\t// action pins.'],
		['rule.py', '# Current artifact\n# action pins.'],
		['rule.mjs', '// Current artifact\n// action pins.'],
		['rule.go', '\tmessage := []string{"Keep this", "independently verified", "Keep both"}'],
	]) {
		const lines = source.split('\n');
		const patch = '@@ -0,0 +1,' + lines.length + ' @@\n' + lines.map(line => '+' + line).join('\n') + '\n';
		const [group] = groupsFromPatch(path, patch);
		assert.ok(group, path);
		for (const match of group.matches) assert.equal(group.text.slice(match.start, match.end), match.text, path);
	}
});

test('avoids fabricating a noun chain across paragraph or literal boundaries', () => {
	assert.equal(wordingMatches('artifact\n\naction pins').some(m => m.ruleId === 'technical-noun-chain'), false);
	assert.deepEqual(groupsFromPatch('rule.go', '@@ -0,0 +1,1 @@\n+labels := []string{"rather", "than"}\n'), []);
});

test('reports every word occurrence and one longest noun-chain span', () => {
	const matches = wordingMatches('Keep artifact action pins. Keep artifact action pins.');
	assert.deepEqual(matches.filter(m => m.ruleId === 'technical-noun-chain').map(m => m.text), ['artifact action pins', 'artifact action pins']);
	assert.deepEqual(matches.filter(m => m.ruleId === 'stock-wording').map(m => m.text), ['Keep', 'pins', 'Keep', 'pins']);
});

test('escapes match text in review bodies', () => {
	const text = 'Keep <script>&value</script>.';
	const matches = [{ ruleId: 'test-rule', label: 'stock technical wording', text, start: 0, end: text.length }];
	const inline = bodyFor({ path: 'rule.go', start: 1, end: 1, text, matches, reasons: ['stock technical wording'] });
	assert.match(inline, /&lt;script&gt;&amp;value&lt;\/script&gt;/);
	assert.doesNotMatch(inline, /<script>/);
	const prose = proseBody(commitFindings('commit', 'Keep values.'));
	assert.match(prose, /`stock-wording`/);
	assert.match(prose, /\[0, 4\)/);
});

test('catalog exposes unique stable IDs with guidance and executable patterns', () => {
	const catalog = ruleCatalog();
	assert.equal(new Set(catalog.map(rule => rule.id)).size, catalog.length);
	for (const rule of catalog) {
		assert.match(rule.id, /^[a-z][a-z0-9-]+$/);
		assert.ok(rule.label);
		assert.ok(rule.guidance);
	}
	assert.ok(catalog.some(rule => rule.id === 'maintenance-claim'));
	assert.ok(catalog.some(rule => rule.id === 'verification-disclaimer'));
});

test('CLI describes its scan boundaries and exports the catalog', () => {
	const script = new URL('./comment-cop.mjs', import.meta.url);
	const help = execFileSync(process.execPath, [script.pathname, '--help'], { encoding: 'utf8' });
	assert.match(help, /--json/);
	assert.match(help, /--rules/);
	assert.match(help, /Suppressions: none/);
	const catalog = JSON.parse(execFileSync(process.execPath, [script.pathname, '--rules'], { encoding: 'utf8' }));
	assert.deepEqual(catalog, ruleCatalog());
	const invalid = spawnSync(process.execPath, [script.pathname, '--suppress=stock-wording']);
	assert.equal(invalid.status, 2);
});

test('empty patterns and differently sized search text fail safely', () => {
	const rule = { id: 'test-rule', label: 'test', pattern: /(?:)/, guidance: 'test' };
	assert.deepEqual(findMatches('text', [rule]), []);
	assert.throws(() => findMatches('text', [rule], 'short'), /source length/);
});

test('CLI exports scan findings as JSON with the normal finding exit codes', () => {
	const script = new URL('./comment-cop.mjs', import.meta.url);
	const result = spawnSync(process.execPath, [script.pathname, '--json', 'HEAD'], {
		cwd: new URL('../../../', import.meta.url), encoding: 'utf8', maxBuffer: 1 << 27,
	});
	assert.ok(result.status === 0 || result.status === 1, result.stderr);
	const report = JSON.parse(result.stdout);
	assert.equal(report.base, 'HEAD');
	assert.equal(report.total, report.groups.length + report.prose.length);
	assert.equal(result.status, report.total > 0 ? 1 : 0);
	for (const finding of [...report.groups, ...report.prose]) {
		for (const match of finding.matches) assert.equal(finding.text.slice(match.start, match.end), match.text);
	}
});
