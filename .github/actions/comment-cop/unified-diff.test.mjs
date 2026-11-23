import assert from 'node:assert/strict';
import test from 'node:test';

import { groupsFromPatch } from './comment-cop.mjs';
import { filesFromDiff } from './unified-diff.mjs';

test('splits a modified file into its hunks', () => {
	const diff = [
		'diff --git a/docs/guide.md b/docs/guide.md',
		'index 1111111..2222222 100644',
		'--- a/docs/guide.md',
		'+++ b/docs/guide.md',
		'@@ -1,2 +1,3 @@',
		' # Guide',
		'+That said, the cache stores values.',
		' Plain text.',
		'@@ -10,1 +11,1 @@',
		'--- removed line that looks like a header',
		'+++ added line that looks like a header',
		'',
	].join('\n');

	assert.deepEqual(filesFromDiff(diff), [{
		kind: 'patched',
		path: 'docs/guide.md',
		patch: [
			'@@ -1,2 +1,3 @@',
			' # Guide',
			'+That said, the cache stores values.',
			' Plain text.',
			'@@ -10,1 +11,1 @@',
			'--- removed line that looks like a header',
			'+++ added line that looks like a header',
		].join('\n'),
	}]);
});

test('names a renamed file by its new path', () => {
	const diff = [
		'diff --git a/docs/old.md b/docs/new.md',
		'similarity index 80%',
		'rename from docs/old.md',
		'rename to docs/new.md',
		'index 1111111..2222222 100644',
		'--- a/docs/old.md',
		'+++ b/docs/new.md',
		'@@ -1 +1,2 @@',
		' x',
		'+Moreover, y.',
		'diff --git a/a b.md b/c d.md',
		'similarity index 100%',
		'rename from a b.md',
		'rename to c d.md',
	].join('\n');

	assert.deepEqual(filesFromDiff(diff), [
		{ kind: 'patched', path: 'docs/new.md', patch: '@@ -1 +1,2 @@\n x\n+Moreover, y.' },
		{ kind: 'patched', path: 'c d.md', patch: '' },
	]);
});

test('marks a deleted file as removed', () => {
	const diff = [
		'diff --git a/docs/gone.md b/docs/gone.md',
		'deleted file mode 100644',
		'index 975fbec..0000000',
		'--- a/docs/gone.md',
		'+++ /dev/null',
		'@@ -1 +0,0 @@',
		'-y',
		'diff --git a/img.png b/img.png',
		'deleted file mode 100644',
		'index 975fbec..0000000',
		'Binary files a/img.png and /dev/null differ',
	].join('\n');

	assert.deepEqual(filesFromDiff(diff), [
		{ kind: 'removed', path: 'docs/gone.md' },
		{ kind: 'removed', path: 'img.png' },
	]);
});

test('marks a binary file as unpatched', () => {
	const diff = [
		'diff --git a/docs/diagram.md b/docs/diagram.md',
		'index bdc955b..8835708 100644',
		'Binary files a/docs/diagram.md and b/docs/diagram.md differ',
		'diff --git a/docs/blob.md b/docs/blob.md',
		'new file mode 100644',
		'index 0000000..8835708',
		'GIT binary patch',
		'literal 2',
		'JcmZPo000310RR91',
		'',
	].join('\n');

	assert.deepEqual(filesFromDiff(diff), [
		{ kind: 'unpatched', path: 'docs/diagram.md' },
		{ kind: 'unpatched', path: 'docs/blob.md' },
	]);
});

test('reads quoted paths and paths with spaces', () => {
	const diff = [
		'diff --git "a/docs/caf\\303\\251.md" "b/docs/caf\\303\\251.md"',
		'index b680253..00989b9 100644',
		'--- "a/docs/caf\\303\\251.md"',
		'+++ "b/docs/caf\\303\\251.md"',
		'@@ -1 +1,2 @@',
		' z',
		'+q',
		'diff --git a/docs/design notes.md b/docs/design notes.md',
		'index 587be6b..49b5575 100644',
		'--- a/docs/design notes.md\t',
		'+++ b/docs/design notes.md\t',
		'@@ -1 +1,2 @@',
		' x',
		'+p',
		'diff --git "a/docs/new \\"q\\".md" "b/docs/new \\"q\\".md"',
		'new file mode 100644',
		'index 0000000..e556b83',
		'--- /dev/null',
		'+++ "b/docs/new \\"q\\".md"\t',
		'@@ -0,0 +1 @@',
		'+w',
		'diff --git "a/docs/tab\\there.md" "b/docs/tab\\there.md"',
		'new file mode 100644',
		'index 0000000..e69de29',
		'diff --git a/docs/empty notes.md b/docs/empty notes.md',
		'new file mode 100644',
		'index 0000000..e69de29',
	].join('\n');

	assert.deepEqual(filesFromDiff(diff).map(file => file.path), [
		'docs/café.md',
		'docs/design notes.md',
		'docs/new "q".md',
		'docs/tab\there.md',
		'docs/empty notes.md',
	]);
});

test('keeps CRLF content, end-of-file markers and hunkless mode changes', () => {
	const diff = [
		'diff --git a/docs/win.md b/docs/win.md',
		'index 1111111..2222222 100644',
		'--- a/docs/win.md',
		'+++ b/docs/win.md',
		'@@ -1 +1,2 @@',
		' a\r',
		'+b',
		'\\ No newline at end of file',
		'diff --git a/scripts/run.sh b/scripts/run.sh',
		'old mode 100644',
		'new mode 100755',
		'diff --git a/x b/y.md b/x b/y.md',
		'index 1111111..2222222 100644',
		'--- a/x b/y.md\t',
		'+++ b/x b/y.md\t',
		'@@ -1 +1 @@',
		'-old',
		'+new',
	].join('\n');

	assert.deepEqual(filesFromDiff(diff), [
		{ kind: 'patched', path: 'docs/win.md', patch: '@@ -1 +1,2 @@\n a\r\n+b\n\\ No newline at end of file' },
		{ kind: 'patched', path: 'scripts/run.sh', patch: '' },
		{ kind: 'patched', path: 'x b/y.md', patch: '@@ -1 +1 @@\n-old\n+new' },
	]);
});

test('rejects a body with no file headers and accepts an empty diff', () => {
	assert.throws(() => filesFromDiff('<!DOCTYPE html>\n<html></html>\n'), /no file headers/);
	assert.throws(() => filesFromDiff('diff --git "a/x\\q" "b/x\\q"\n'), /unknown escape/);
	assert.throws(() => filesFromDiff('diff --git "a/x "b/x"\n--- "a/x\n'), /unterminated|ambiguous/);
	assert.deepEqual(filesFromDiff(''), []);
});

test('gives groupsFromPatch the same patch that the files API reports', () => {
	const patch = '@@ -0,0 +1,3 @@\n+\t// Parse the value here.\n+\t// Keep the original around.\n+\t// Return both values.';
	const diff = `diff --git a/rule.go b/rule.go\nnew file mode 100644\nindex 0000000..1111111\n--- /dev/null\n+++ b/rule.go\n${patch}\n`;
	const [file] = filesFromDiff(diff);

	assert.equal(file.kind, 'patched');
	assert.equal(file.kind === 'patched' ? file.patch : undefined, patch);
	assert.deepEqual(groupsFromPatch(file.path, patch).map(group => group.reasons), [['3 lines', 'stock technical wording']]);
});
