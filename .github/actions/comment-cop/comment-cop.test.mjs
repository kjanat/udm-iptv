import assert from 'node:assert/strict';
import test, { mock } from 'node:test';

import run, { bodyFor, commitFindings, groupsFromPatch, keyFor, proseBody, proseFindings } from './comment-cop.mjs';

/** @typedef {Parameters<Parameters<typeof run>[0]['github']['rest']['pulls']['createReview']>[0]} ReviewParams */

test('flags a long implementation comment', () => {
	const groups = groupsFromPatch(
		'rule.go',
		`\
@@ -0,0 +1,4 @@
+\t// Parse the value here.
+\t// Keep the original around.
+\t// Return both values.
+\tparse(value)
`,
	);

	assert.deepEqual(groups.map(group => group.reasons), [['3 lines']]);
});

test('does not measure Go doc comments by length', () => {
	const groups = groupsFromPatch(
		'rule.go',
		`\
@@ -0,0 +1,4 @@
+// Parser reads workflows.
+// It reports invalid syntax.
+// It returns every diagnostic.
+type Parser struct{}
`,
	);

	assert.deepEqual(groups, []);
});

test('does not measure Go field documentation by length', () => {
	const patch = [
		'@@ -1,2 +1,5 @@',
		' type Metadata struct {',
		'+\t// Defaults holds input values.',
		'+\t// Each value includes its position.',
		'+\t// Values remain in source order.',
		'+\tDefaults []*Value',
		' }',
	].join('\n');

	assert.deepEqual(groupsFromPatch('metadata.go', patch), []);
});

test('recognizes a Go doc comment when its declaration follows unchanged lines', () => {
	const source = [
		'// Check scans expressions.',
		'// It preserves source positions.',
		'// It reports unavailable contexts.',
		'// Invalid expressions end the scan.',
		'func Check() {}',
	].join('\n');
	const patch = [
		'@@ -1,3 +1,5 @@',
		'-// Check scans source.',
		'+// Check scans expressions.',
		'+// It preserves source positions.',
		'+// It reports unavailable contexts.',
		' // Invalid expressions end the scan.',
		' func Check() {}',
	].join('\n');

	assert.deepEqual(groupsFromPatch('rule.go', patch, source), []);
});

test('uses the unchanged first line to recognize partial field documentation', () => {
	const source = [
		'type Metadata struct {',
		'\t// Defaults holds input values.',
		'\t// Each value includes its position.',
		'\t// Values remain in source order.',
		'\t// The checker reads these values.',
		'\tDefaults []*Value',
		'}',
	].join('\n');
	const patch = [
		'@@ -2,2 +2,5 @@',
		' \t// Defaults holds input values.',
		'+\t// Each value includes its position.',
		'+\t// Values remain in source order.',
		'+\t// The checker reads these values.',
		' \tDefaults []*Value',
	].join('\n');

	assert.deepEqual(groupsFromPatch('metadata.go', patch, source), []);
});

test('flags style tells at any length', () => {
	const groups = groupsFromPatch(
		'rule.go',
		`\
@@ -0,0 +1,2 @@
+\t// Use the cache rather than parsing twice.
+\treturn cache
`,
	);

	assert.deepEqual(groups.map(group => group.reasons), [['"X rather than Y"']]);
});

test('flags hedging in comments and Markdown', () => {
	const comment = groupsFromPatch(
		'rule.go',
		`\
@@ -0,0 +1,2 @@
+\t// The lease may not be present yet.
+\treturn lease
`,
	);
	const prose = groupsFromPatch(
		'docs/status.md',
		`\
@@ -0,0 +1,1 @@
+Playback remains unknown until a receiver reports.
`,
	);

	assert.deepEqual(comment.map(group => group.reasons), [['hedge']]);
	assert.deepEqual(prose.map(group => group.reasons), [['hedge']]);
});

test('flags participial post-modifiers and reduced relative clauses', () => {
	for (
		const text of [
			'The marker the daemon writes is owned state.',
			'A page lists its import map and the origin serving it.',
			'Keep a build of the commit deployed by the workflow.',
			'Count the pages that version ships.',
			'Count the pages this version ships.',
		]
	) {
		assert.deepEqual(proseFindings('commit 1234567', text).map(finding => finding.reasons), [['participial post-modifier']], text);
	}
	for (
		const text of [
			'The marker that the daemon writes is owned state.',
			'A page lists its import map and its origin.',
			'The following page lists the remaining addresses.',
			'The test failed.',
			'The daemon is running on the uplink.',
			'A finding posts a review that requests changes.',
		]
	) {
		assert.deepEqual(proseFindings('commit 1234567', text), [], text);
	}
});

test('flags a commit subject over fifty characters', () => {
	const long = `ci: ${'x'.repeat(47)}\n\nBody.`;
	assert.deepEqual(commitFindings('commit 1234567', long).map(finding => finding.reasons), [[
		'subject longer than 50 characters',
	]]);
	assert.deepEqual(commitFindings('commit 1234567', `ci: ${'x'.repeat(46)}\n\nBody.`), []);
	const both = `${'x'.repeat(51)}\n\nKeep the origin serving it.`;
	assert.deepEqual(commitFindings('commit 1234567', both).map(finding => finding.reasons), [[
		'subject longer than 50 characters',
		'participial post-modifier',
	]]);
});

test('flags tells inside Go string literals', () => {
	const groups = groupsFromPatch(
		'status.go',
		`\
@@ -0,0 +1,3 @@
+\tmatched := "VLAN configuration matches (DHCP and playback not verified)"
+\tname := fmt.Sprintf("%s on %s", link, parent)
+\treturn matched + name
`,
	);

	assert.deepEqual(groups.map(group => [group.start, group.reasons]), [[1, ['hedge']]]);
});

test('ignores Go struct tags and strings without tells', () => {
	const groups = groupsFromPatch(
		'status.go',
		`\
@@ -0,0 +1,3 @@
+type vlanCheck struct {
+\tStatus string \`json:"status,omitempty"\`
+}
`,
	);

	assert.deepEqual(groups, []);
});

test('scans Markdown prose but skips fenced code', () => {
	const groups = groupsFromPatch(
		'docs/checks.md',
		`\
@@ -0,0 +1,7 @@
+That said, this paragraph is prose.
+
+\`\`\`go
+// This comment uses robust machinery.
+\`\`\`
+
+- Moreover, this item is separate.
`,
	);

	assert.deepEqual(
		groups.map(group => group.reasons),
		[['filler phrase'], ['connective glue']],
	);
});

test('restores Markdown fence state before each diff hunk', () => {
	const source = [
		'# Example',
		'',
		'````markdown',
		'This contains a shorter ``` marker.',
		'Still inside the fence.',
		'That said, this is code-fence content.',
		'````',
	].join('\n');
	const patch = [
		'@@ -5,0 +6,1 @@',
		'+That said, this is code-fence content.',
	].join('\n');

	assert.deepEqual(groupsFromPatch('docs/checks.md', patch, source), []);
});

test('ignores unsupported file types', () => {
	const groups = groupsFromPatch(
		'fixture.txt',
		`\
@@ -0,0 +1,3 @@
+// one
+// two
+// three
`,
	);

	assert.deepEqual(groups, []);
});

test('uses opaque location-specific marker keys', () => {
	const groups = groupsFromPatch(
		'docs/design notes.md',
		`\
@@ -0,0 +1,1 @@
+That said, repeated prose.
@@ -9,0 +10,1 @@
+That said, repeated prose.
`,
	);

	const keys = groups.map(keyFor);
	assert.equal(keys.length, 2);
	assert.match(keys[0], /^[a-f0-9]{16}$/);
	assert.match(keys[1], /^[a-f0-9]{16}$/);
	assert.notEqual(keys[0], keys[1]);
});

test('tailors advice to the finding and keeps contributor guidance in a sub footer', () => {
	const group = { path: 'rule.go', start: 1, end: 3, text: '// Explanation', reasons: ['3 lines'] };
	const lengthBody = bodyFor(group);
	const consequenceBody = bodyFor({ ...group, reasons: ['counterfactual justification'] });

	assert.match(lengthBody, /length-only flag/);
	assert.doesNotMatch(consequenceBody, /length-only flag/);
	assert.match(consequenceBody, /consequence or failure mode/);
	assert.match(consequenceBody, /<sub>[^<]*advisory[^<]*resolve this thread[^<]*<\/sub>/);
	assert.match(consequenceBody, /<br><sub>Any AI agents[^<]*justify the closure with a reply[^<]*<\/sub>$/);
});

test('combines different advice and deduplicates equivalent contrast advice', () => {
	const group = {
		path: 'rule.go',
		start: 1,
		end: 3,
		text: '// Explanation',
		reasons: ['3 lines', '"X instead of Y"', '"X rather than Y"'],
	};
	const body = bodyFor(group);

	assert.equal(body.split('\n').filter(line => line.startsWith('- ')).length, 2);
	assert.match(body, /length-only flag/);
	assert.match(body, /comparison explains a real constraint/);
});

const reviewFiles = [
	{
		filename: 'docs/first.md',
		status: 'modified',
		contents_url: 'first',
		patch:
			'@@ -0,0 +1,5 @@\n+That said, the cache stores values.\n+Read the cached entry.\n+\n+\n+Moreover, refresh the entry.',
	},
	{
		filename: 'docs/second.md',
		status: 'modified',
		contents_url: 'second',
		patch: '@@ -0,0 +1,1 @@\n+Use the cache rather than fetching again.',
	},
];
const firstGroup = {
	path: 'docs/first.md',
	start: 1,
	end: 2,
	text: 'That said, the cache stores values.\nRead the cached entry.',
	reasons: ['filler phrase'],
};

const cleanCommit = { sha: 'c'.repeat(40), commit: { message: 'docs: add a policy\n\nPlain body.' } };
const flaggedCommit = { sha: 'b'.repeat(40), commit: { message: 'Keep the origin serving it\n\nBody.' } };

/** @param {string[]} seenBodies @param {Error | undefined} submitError */
function reviewHarness(seenBodies = [], submitError = undefined, files = reviewFiles, commits = [cleanCommit], reviews = []) {
	const createReview = mock.fn(async (/** @type {ReviewParams} */ params) => {
		if (submitError) throw submitError;
		return params;
	});
	const listFiles = mock.fn();
	const listCommits = mock.fn();
	const listReviews = mock.fn();
	const dismissReview = mock.fn(async params => params);
	const warning = mock.fn();
	const info = mock.fn();
	const setFailed = mock.fn();
	const graphql = mock.fn(async () => ({
		repository: {
			pullRequest: {
				reviewThreads: {
					pageInfo: { hasNextPage: false, endCursor: null },
					nodes: seenBodies.map((body, i) => ({
						id: `thread-${i}`,
						isResolved: false,
						path: 'docs/first.md',
						comments: { nodes: [{ body, viewerDidAuthor: true }] },
					})),
				},
			},
		},
	}));
	const args = {
		github: {
			rest: { pulls: { listFiles, listCommits, listReviews, dismissReview, createReview } },
			paginate: mock.fn(async (/** @type {unknown} */ fn) => {
				if (fn === listFiles) return files;
				if (fn === listCommits) return commits;
				if (fn === listReviews) return reviews;
				return [];
			}),
			request: mock.fn(async () => ({ data: 'Plain Markdown with no code fences.' })),
			graphql,
		},
		context: {
			repo: { owner: 'owner', repo: 'repo' },
			serverUrl: 'https://github.com',
			payload: { pull_request: { number: 42, head: { sha: 'a'.repeat(40) }, body: 'Plain description.' } },
		},
		core: { warning, info, setFailed },
	};
	return {
		args: /** @type {Parameters<typeof run>[0]} */ (/** @type {unknown} */ (args)),
		createReview,
		dismissReview,
		warning,
		info,
		setFailed,
		graphql,
	};
}

/** @param {ReturnType<typeof reviewHarness>} h */
const requestedChanges = h =>
	h.createReview.mock.calls.map(call => call.arguments[0]).filter(params => params.event === 'REQUEST_CHANGES');

test('requests changes for commit messages and the description, fails the job, and clears itself', async () => {
	const h = reviewHarness([], undefined, [], [flaggedCommit, cleanCommit]);
	h.args.context.payload.pull_request.body = 'Describes the change, not the diff.';
	await run(h.args);

	const [review, ...others] = requestedChanges(h);
	assert.equal(others.length, 0);
	assert.equal(review.commit_id, 'a'.repeat(40));
	assert.match(review.body, /^<!-- actionlint-comment-cop:prose -->/);
	assert.match(
		review.body,
		/\*\*\[commit bbbbbbb\]\(https:\/\/github\.com\/owner\/repo\/commit\/b{40}\)\*\*, flagged for: participial post-modifier\./,
	);
	assert.match(review.body, /\*\*pull request description\*\*, flagged for: "X, not Y"\./);
	assert.doesNotMatch(review.body, /ccccccc/);
	assert.deepEqual(h.setFailed.mock.calls[0].arguments, ['Comment Cop: 2 finding(s) in the commit messages or the description.']);
	assert.match(h.info.mock.calls[0].arguments[0], /2 in commit messages and the description/);

	const existing = [{ id: 7, state: 'CHANGES_REQUESTED', body: review.body }];
	const unchanged = reviewHarness([], undefined, [], [flaggedCommit, cleanCommit], existing);
	unchanged.args.context.payload.pull_request.body = 'Describes the change, not the diff.';
	await run(unchanged.args);
	assert.equal(requestedChanges(unchanged).length, 0);
	assert.equal(unchanged.dismissReview.mock.callCount(), 0);
	assert.equal(unchanged.setFailed.mock.callCount(), 1);

	const clean = reviewHarness([], undefined, [], [cleanCommit], existing);
	await run(clean.args);
	assert.equal(requestedChanges(clean).length, 0);
	assert.deepEqual(clean.dismissReview.mock.calls[0].arguments[0], {
		owner: 'owner',
		repo: 'repo',
		pull_number: 42,
		review_id: 7,
		message: 'Comment Cop: the commit messages and the description are clean.',
	});
	assert.equal(clean.setFailed.mock.callCount(), 0);
	assert.equal(proseBody([]).startsWith('<!-- actionlint-comment-cop:prose -->'), true);
});

test('submits comments across files and line ranges in one review', async () => {
	const h = reviewHarness();
	await run(h.args);

	assert.equal(h.createReview.mock.callCount(), 1);
	const params = h.createReview.mock.calls[0].arguments[0];
	assert.deepEqual(params, {
		owner: 'owner',
		repo: 'repo',
		pull_number: 42,
		commit_id: 'a'.repeat(40),
		event: 'COMMENT',
		body: 'Please review the flagged wording in the inline comments.',
		comments: [
			{ path: 'docs/first.md', line: 2, side: 'RIGHT', body: bodyFor(firstGroup), start_line: 1, start_side: 'RIGHT' },
			{
				path: 'docs/first.md',
				line: 5,
				side: 'RIGHT',
				body: bodyFor({
					...firstGroup,
					start: 5,
					end: 5,
					text: 'Moreover, refresh the entry.',
					reasons: ['connective glue'],
				}),
			},
			{
				path: 'docs/second.md',
				line: 1,
				side: 'RIGHT',
				body: bodyFor({
					path: 'docs/second.md',
					start: 1,
					end: 1,
					text: 'Use the cache rather than fetching again.',
					reasons: ['"X rather than Y"'],
				}),
			},
		],
	});
	assert.equal(h.warning.mock.callCount(), 0);
	assert.match(h.info.mock.calls[0].arguments[0], /3 posted/);

	assert.ok(params.comments);
	const rerun = reviewHarness(params.comments.map(comment => comment.body));
	await run(rerun.args);
	assert.equal(rerun.createReview.mock.callCount(), 0);
	assert.equal(rerun.graphql.mock.callCount(), 1);
});

test('excludes existing findings from the next review', async () => {
	const h = reviewHarness([bodyFor(firstGroup)]);
	await run(h.args);

	assert.equal(h.createReview.mock.callCount(), 1);
	const params = h.createReview.mock.calls[0].arguments[0];
	assert.ok(params.comments);
	assert.deepEqual(params.comments.map(({ path, line }) => ({ path, line })), [
		{ path: 'docs/first.md', line: 5 },
		{ path: 'docs/second.md', line: 1 },
	]);
});

test('does not submit an empty review when the diff has no findings', async () => {
	const h = reviewHarness([], undefined, []);
	await run(h.args);

	assert.equal(h.createReview.mock.callCount(), 0);
	assert.match(h.info.mock.calls[0].arguments[0], /0 posted/);
});

test('reports a failed batch without submitting individual reviews', async () => {
	const h = reviewHarness([], new Error('Review rejected'));
	await run(h.args);

	assert.equal(h.createReview.mock.callCount(), 1);
	assert.deepEqual(h.warning.mock.calls[0].arguments, ['Could not submit Comment Cop review: Review rejected']);
	assert.match(h.info.mock.calls[0].arguments[0], /0 posted/);
});
