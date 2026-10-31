import assert from 'node:assert/strict';
import test from 'node:test';

import { bodyFor, commitFindings, groupsFromPatch, proseFindings } from './comment-cop.mjs';

const contrast = '"X, not Y"';

test('recognizes the README assurance as implicit contrast', () => {
	const text = '`start` resumes early; running services remain uninterrupted.';
	assert.deepEqual(proseFindings('README', text).map(f => f.reasons), [[contrast, 'semicolon-joined prose', 'stock technical wording']]);
	assert.deepEqual(groupsFromPatch('README.md', `@@ -0,0 +1,1 @@\n+${text}\n`).map(g => g.reasons), [[contrast, 'semicolon-joined prose', 'stock technical wording']]);
});

test('flags assurances of unchanged state across grammatical forms', () => {
	for (const text of [
		'Running services remain uninterrupted.',
		'Running services remain entirely unaffected.',
		'The configuration stays unchanged.',
		'The file stayed unmodified.',
		'The cache is otherwise untouched.',
		'The addresses will be unchanged.',
		'The route was left intact.',
		'The service is left as-is.',
		'The file will stay the same.',
		'The daemon leaves all other interfaces untouched.',
		'Leave the configuration unchanged.',
		'Keeping the addresses intact.',
		'Keeps the files the same.',
		'WithInterfaces leaves a LAN list it is given alone.',
		'The service continues uninterrupted.',
		'The service continues to operate normally.',
		'Existing sessions continue as before.',
		'The sessions continue as usual.',
	]) {
		const findings = proseFindings('assurance', text);
		assert.equal(findings.some(f => f.reasons.includes(contrast)), true, text);
	}
});

test('flags non-effect assurances written with without or explicit negation', () => {
	for (const text of [
		'Restarts without affecting running services.',
		'Applies the lease without changing other addresses.',
		'Writes the config without modifying the unit.',
		'Completes without any interruption.',
		'Finishes with no side effects.',
		'Continues without impact on running services.',
		'The update does not interrupt existing streams.',
		'The update will not restart other services.',
		"The command doesn't alter the file.",
		"This won't change the address.",
		'The service resumes, NOT interrupts playback.',
	]) {
		assert.equal(proseFindings('assurance', text).some(f => f.reasons.includes(contrast)), true, text);
	}
});

test('requires a non-effect predicate rather than a negative-looking adjective alone', () => {
	for (const text of [
		'The service is unavailable.',
		'The interface remains unresolved.',
		'The lease is unknown.',
		'The cache is untrusted.',
		'The parser reads unchanged lines.',
		'The daemon restarts after failure.',
		'The service stays active.',
		'Leave a marker on disk.',
		'Without a lease, the daemon waits.',
		'Read the file without a trailing newline.',
		'Read the configuration without comments.',
		'The name is not valid.',
	]) {
		assert.equal(proseFindings('neutral', text).some(f => f.reasons.includes(contrast)), false, text);
	}
});

test('handles line wrapping and independent string literals', () => {
	const message = 'Describe startup\n\nThe service stays\nunchanged.';
	assert.equal(commitFindings('commit', message)[0].reasons.includes(contrast), true);
	const groups = groupsFromPatch('status.go', '@@ -0,0 +1,1 @@\n+message := "Services remain uninterrupted."\n');
	assert.equal(groups[0].reasons.includes(contrast), true);
	assert.deepEqual(groupsFromPatch('status.go', '@@ -0,0 +1,1 @@\n+fields := []string{"remain", "unchanged"}\n').map(g => g.reasons), [['stock technical wording']]);
});

test('deduplicates explicit and implicit contrast with the existing guidance', () => {
	const findings = proseFindings('description', 'It resumes, not restarts. Sessions stay unchanged.');
	assert.deepEqual(findings.map(f => f.reasons), [[contrast]]);
	const body = bodyFor({ path: 'README.md', start: 1, end: 1, text: 'Sessions stay unchanged.', reasons: [contrast] });
	assert.match(body, /comparison explains a real constraint/);
});

test('detects verification disclaimers beyond the literal not verified', () => {
	for (const text of [
		'Playback not verified.',
		'Playback NOT VERIFIED.',
		'Playback has not been verified.',
		'Playback was not independently confirmed.',
		"Playback hasn't yet been tested.",
		"This wasn't manually checked.",
		'Not fully validated on hardware.',
		'Not yet checked.',
		'Untested hardware behavior.',
		'Unverified implementation.',
		'Unvalidated configuration.',
		'Unconfirmed outcome.',
		'Cannot confirm playback.',
		'Cannot be verified.',
		'Cannot be independently verified.',
		"Can't prove playback works.",
		'Unable to verify playback.',
		'Does not prove playback works.',
		"This doesn't establish downstream reception.",
		'Verification was not performed.',
		'Testing has not been done.',
		'Checks were not run.',
		'Validation is pending.',
	]) {
		assert.equal(proseFindings('verification', text).some(f => f.reasons.includes('verification disclaimer')), true, text);
	}
	assert.equal(commitFindings('commit', 'Describe playback\n\nNot independently\nverified.')[0].reasons.includes('verification disclaimer'), true);
});

test('distinguishes missing verification from affirmative checks and unrelated negation', () => {
	for (const text of [
		'Playback was verified on the receiver.',
		'Verify interface registration before activation.',
		'The file was not found.',
		'Not all packets are multicast.',
		'The certificate cannot be decoded.',
		'Testing is complete.',
	]) {
		assert.equal(proseFindings('neutral', text).some(f => f.reasons.includes('verification disclaimer')), false, text);
	}
	assert.deepEqual(proseFindings('wording', 'The checker runs independently.').map(f => f.reasons), [['stock technical wording']]);
});
