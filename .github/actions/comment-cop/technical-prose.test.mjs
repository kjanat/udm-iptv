import assert from 'node:assert/strict';
import test from 'node:test';

import { bodyFor, commitFindings, groupsFromPatch, proseBody, proseFindings, wordingMatches } from './comment-cop.mjs';
import { stockReasons, technicalReasons } from './technical-prose.mjs';

test('flags every requested word and inflection in both cases', () => {
	for (const word of [
		'sequence', 'sequences', 'sequencing', 'retain', 'retains', 'retained', 'retaining',
		'support', 'supports', 'supported', 'supporting', 'pin', 'pins', 'pinned', 'pinning',
		'remain', 'remains', 'remaining', 'record', 'records', 'recorded', 'recording',
		'cannot', 'never', 'named', 'Keep', 'legacy', 'distinct', 'distinctly', 'avoids',
		'avoid', 'avoiding', 'evidence', 'independently', 'does not', 'does\nnot', 'does.not', 'Retain',
	]) {
		assert.deepEqual(stockReasons('The description says ' + word + '.'), ['stock technical wording'], word);
		assert.deepEqual(stockReasons(word.toUpperCase()), ['stock technical wording'], word);
	}
});

test('groups every stock occurrence under one reason', () => {
	const [finding] = commitFindings('commit', 'Keep legacy behavior\n\nRetain evidence. Never discard evidence.');
	assert.deepEqual(finding.reasons, ['stock technical wording']);
	assert.deepEqual(finding.matches.map(m => m.text), ['Keep', 'legacy', 'Retain', 'evidence', 'Never', 'evidence']);
	assert.match(proseBody([finding]), /Describe the specific operation or constraint/);
	assert.match(proseBody([finding]), /Matches:/);
});

test('uses word boundaries for unrelated words', () => {
	for (const text of ['Bookkeeping is enabled.', 'The option is unsupported.', 'The example is a pinnacle.']) {
		assert.deepEqual(stockReasons(text), [], text);
	}
});

test('quotes, inline identifiers and indentation supply zero wording exemptions', () => {
	for (const text of [
		'Use `retainState`.',
		'Example: "retain".',
		"Example: 'retain'.",
		'Example: `Keep legacy support`.',
		'> Keep legacy support.',
		'    Keep legacy support.',
		'Test parser\n\n```text\nKeep legacy support.\n```',
	]) {
		assert.deepEqual(stockReasons(text), ['stock technical wording'], text);
	}
});

test('scans each Go literal separately and combines its reasons once', () => {
	for (const source of [
		'for _, field := range [][2]string{{"version", item.Version}, {"package", item.Package}, {"package version", item.PackageVersion}} {',
		'assertEqual(t, "runtime version", fmt.Sprint(event.Contexts["runtime"]["version"]), runtime.Version())',
	]) {
		assert.deepEqual(groupsFromPatch('metadata.go', '@@ -0,0 +1,1 @@\n+' + source + '\n'), [], source);
	}
	const source = 'message := []string{"Keep this", "never do that", "Keep both"}';
	const [group] = groupsFromPatch('metadata.go', '@@ -0,0 +1,1 @@\n+' + source + '\n');
	assert.deepEqual(group.reasons, ['stock technical wording']);
	assert.deepEqual(group.matches.map(m => m.text), ['Keep', 'never', 'Keep']);
});

test('flags runtime descriptions that contain temporal qualifiers', () => {
	for (const text of [
		'Install the latest release on the console:',
		'release version or latest',
		'resolve latest release: %v',
		'Read the current address.',
		'Preserve the existing configuration.',
		'Global `latest` tracks UDM Pro. Existing versions apply.',
	]) {
		assert.equal(technicalReasons(text).includes('temporal vagueness'), true, text);
	}
});

test('identifies all discussed defects in the original merge body', () => {
	const message = 'Merge master into go\n\nRetain the Go build and release workflows with the current artifact\naction pins. Master carries the same artifact version updates.';
	const [finding] = commitFindings('commit 30e0011', message);
	assert.deepEqual(finding.reasons, ['opaque technical noun chain', 'temporal vagueness', 'stock technical wording']);
	assert.deepEqual(finding.matches.filter(m => m.ruleId === 'technical-noun-chain').map(m => m.text), ['artifact\naction pins', 'artifact version updates']);
	assert.deepEqual(commitFindings('merge', 'Merge master into go'), []);
});

test('matches noun chains in mixed and wrapped phrasing', () => {
	for (const text of [
		'Current artifact action pins.',
		'Retain artifact action pins.',
		'Keep the existing package version updates.',
		'Adjust runtime state management policy.',
		'Keep the artifact\naction pins.',
		'Image name aliases.',
	]) {
		assert.equal(technicalReasons(text).includes('opaque technical noun chain'), true, text);
	}
});

test('versions, SHAs, paths and nearby references supply zero exemptions', () => {
	for (const text of [
		'Keep the current pins from master.',
		'Use current actions/upload-artifact pins.',
		'Keep existing image tags at v8.0.2.',
		'Update `actions/upload-artifact`. Keep the current pins.',
		'Use v8.0.2. Keep the current pins.',
		'Keep the current pins on `release/go`.',
	]) {
		assert.equal(technicalReasons(text).includes('temporal vagueness'), true, text);
	}
	assert.equal(technicalReasons('Retain artifact action pins from 123abcd.').includes('opaque technical noun chain'), true);
});

test('flags naming assertions with every applicable rule', () => {
	const [finding] = proseFindings('naming', 'Existing image names remain canonical aliases.');
	assert.deepEqual(finding.reasons, ['temporal vagueness', 'naming authority', 'stock technical wording']);
	assert.deepEqual(finding.matches.map(m => m.text), ['Existing', 'canonical aliases', 'remain']);
	for (const text of ['Authoritative aliases.', 'Primary names.', 'A source of truth.']) {
		assert.equal(technicalReasons(text).includes('naming authority'), true, text);
	}
});

test('flags vague maintenance independently of subject length', () => {
	for (const message of [
		'build: align toolchains and refresh Nix dependencies',
		'build: align toolchains',
		'build: refresh Nix dependencies',
		'Adjust the local build settings.',
		'Modernize development tooling.',
		'Synchronize the CI environments.',
		'Improve configuration handling.',
	]) {
		assert.equal(commitFindings('commit', message)[0].reasons.includes('vague maintenance claim'), true, message);
	}
	const text = 'build: align toolchains and refresh Nix dependencies';
	assert.deepEqual(wordingMatches(text).filter(m => m.ruleId === 'maintenance-claim').map(m => m.text), ['align toolchains', 'refresh Nix dependencies']);
	assert.deepEqual(commitFindings('commit', 'Use Go 1.27.3 in CI'), []);
	assert.equal(technicalReasons('Align toolchains to Go 1.27.3.').includes('vague maintenance claim'), true);
});

test('flags semicolon prose and parses Markdown entities', () => {
	assert.equal(technicalReasons('Save the file; restart the daemon.').includes('semicolon-joined prose'), true);
	assert.equal(technicalReasons('Save the file;restart the daemon.').includes('semicolon-joined prose'), true);
	for (const text of ['Read &amp; write.', 'Read &#38; write.', 'Read &#x26; write.']) {
		assert.equal(technicalReasons(text).includes('semicolon-joined prose'), false, text);
	}
});

test('scans technical wording across all supported prose entrypoints', () => {
	for (const [path, source] of [
		['rule.go', '\t// Keep the current pins.'],
		['rule.mjs', '// Keep the current pins.'],
		['action.yml', '# Keep the current pins.'],
		['rule.go', '\tmessage := "Keep the current pins."'],
		['README.md', 'Keep the current pins.'],
	]) {
		const groups = groupsFromPatch(path, '@@ -0,0 +1,1 @@\n+' + source + '\n');
		assert.deepEqual(groups.map(g => g.reasons), [['temporal vagueness', 'stock technical wording']], path);
	}
});

test('gives direct guidance with precise matched spans', () => {
	const [finding] = commitFindings('merge', 'Merge branches\n\nRetain current artifact action pins.');
	const rendered = proseBody([finding]);
	assert.match(rendered, /Expand the noun chain/);
	assert.match(rendered, /Specify the baseline/);
	assert.match(rendered, /technical-noun-chain/);
	assert.doesNotMatch(rendered, /Review the flagged wording in context/);
	const body = bodyFor({ path: 'rule.go', start: 1, end: 1, text: 'latest', reasons: ['temporal vagueness'] });
	assert.match(body, /baseline, release, revision, or time period/);
});
