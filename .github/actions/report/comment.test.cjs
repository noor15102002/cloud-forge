'use strict'

const assert = require('node:assert/strict')
const test = require('node:test')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const vm = require('node:vm')

const {
  commentBody,
  marker,
  maximumBodyBytes,
  updateComment,
  validateBody,
  validatePullRequestNumber
} = require('./comment.cjs')

test('external composite reporter resolves its own module instead of the nested action or consumer checkout', async () => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'cloudforge-external-reporter-'))
  try {
    const action = fs.readFileSync(path.join(__dirname, 'action.yml'), 'utf8')
    assert.match(action, /CLOUDFORGE_REPORTER_ACTION_PATH: \$\{\{ github\.action_path \}\}/)
    const script = action.split('        script: |\n')[1].split('\n').map(line => line.replace(/^          /, '')).join('\n')
    fs.copyFileSync(path.join(__dirname, 'comment.cjs'), path.join(directory, 'comment.cjs'))
    const markdown = path.join(directory, 'report.md')
    fs.writeFileSync(markdown, `${marker}\n## Report\n**Status:** FAIL\n`)
    const calls = []
    const github = fakeGitHub([], calls)
    const result = await vm.runInNewContext(`(async () => {${script}\n})()`, {
      require, github,
      process: {env: {
        GITHUB_ACTION_PATH: '/unrelated/nested/github-script',
        GITHUB_WORKSPACE: '/unrelated/consumer',
        CLOUDFORGE_REPORTER_ACTION_PATH: directory,
        CLOUDFORGE_MARKDOWN_PATH: markdown,
        CLOUDFORGE_PR_NUMBER: '7'
      }},
      context: {repo: {owner: 'owner', repo: 'repo'}, payload: {}, serverUrl: 'https://github.com', runId: 123}
    })
    assert.equal(result, '99')
    assert.ok(calls.some(call => call.operation === 'create'))
  } finally {
    fs.rmSync(directory, {recursive: true, force: true})
  }
})

test('keeps rendered apostrophes, quotes, pipes, Unicode and neutralized markup unchanged', () => {
  const body = `${marker}\n## CloudForge verification\n\n**Status:** FAIL · **Application:** worker's "ready" — yes&#124;no &amp; &lt;unknown&gt;\n\n| Experiment | Status | Duration | Result |\n|---|---:|---:|---|\n| worker's "ready" | **FAIL** | 1 ms | one &#124; two &amp; three — &#64;team |\n`
  assert.equal(commentBody(body, 'https://github.com/owner/repo/actions/runs/123'), body)
  assert.ok(!body.includes('&&#35;39;'))
})

test('large reports retain verdict, earlier failure, scan summary and important findings in bounded fallback', () => {
  const header = `${marker}\n## CloudForge verification\n\n**Status:** ERROR · **Application:** worker's "ready" — example\n\n### Container scan\n\n**Status:** WARN\n\n- **Findings:** 235\n- **High:** 52\n- **Known fix available:** 8\n- **Scan scope:** image&#95;a\n\n### Completed evidence\n\n| Experiment | Status | Duration | Result |\n|---|---:|---:|---|\n`
  const rows = [
    '| Earlier application experiment | **FAIL** | 1 ms | connection&#95;closed&#58; 1 / 14 |',
    '| Baseline restoration | **PASS** | 1 ms | restored |',
    '| Cleanup | **ERROR** | 1 ms | inventory incomplete |',
    ...Array.from({length: 600}, (_, index) => `| Evidence ${index} | **WARN** | 1 ms | ${'é — '.repeat(100)} |`)
  ]
  const body = `${header}${rows.join('\n')}\n\n<details>\n<summary>Findings</summary>\n\n### security.critical · WARN/CRITICAL\n\nTrivy reported a critical finding, not confirmed exploitability.\n\n</details>\n`
  assert.ok(Buffer.byteLength(body, 'utf8') > maximumBodyBytes)
  const compact = commentBody(body, 'https://github.com/owner/repo/actions/runs/123')
  assert.ok(Buffer.byteLength(compact, 'utf8') <= maximumBodyBytes)
  assert.ok(compact.startsWith(`${marker}\n`))
  for (const expected of ["**Status:** ERROR · **Application:** worker's", 'Earlier application experiment | **FAIL**', 'Cleanup | **ERROR**', '1 PASS', '**Findings:** 235', '**Known fix available:** 8', 'security.critical', 'https://github.com/owner/repo/actions/runs/123', 'additional non-passing evidence']) {
    assert.ok(compact.includes(expected), `missing ${expected}`)
  }
  assert.ok(!compact.includes('Measurements ('))
  assert.ok(!compact.includes('\uFFFD'))
})

test('compact fallback never emits an arbitrary artifact link', () => {
  const body = `${marker}\n${'x'.repeat(maximumBodyBytes)}`
  for (const artifactURL of ['javascript:alert(1)', 'https://user:secret@github.com/owner/repo/actions/runs/1', 'https://github.com/o/r/actions/runs/1)@team', 'https://example.test/secret']) {
    const compact = commentBody(body, artifactURL)
    assert.ok(compact.includes('retained in the workflow artifact'))
    assert.ok(!compact.includes(artifactURL))
  }
})

test('publishes a large report through the existing bot-owned comment update path', async () => {
  const calls = []
  const github = fakeGitHub([{id: 10, user: {login: 'github-actions[bot]'}, body: `${marker}\nold`}], calls)
  let posted
  github.rest.issues.updateComment = async ({body}) => { posted = body }
  const body = `${marker}\n## CloudForge verification\n\n**Status:** FAIL\n\n${'é'.repeat(maximumBodyBytes)}`
  const id = await updateComment({github, owner: 'owner', repo: 'repo', pullRequestNumber: 7, body, artifactURL: 'https://github.com/owner/repo/actions/runs/123'})
  assert.equal(id, '10')
  assert.ok(posted.includes('**Status:** FAIL'))
  assert.ok(Buffer.byteLength(posted, 'utf8') <= maximumBodyBytes)
  assert.ok(posted.includes('download the workflow artifact'))
})

test('validates pull request numbers and marked bounded bodies', () => {
  assert.equal(validatePullRequestNumber('42'), 42)
  assert.doesNotThrow(() => validateBody(`${marker}\n## Report`))
  assert.throws(() => validatePullRequestNumber('0'), /invalid pull request/)
  assert.throws(() => validatePullRequestNumber(' 42'), /invalid pull request/)
  assert.throws(() => validatePullRequestNumber('0x2a'), /invalid pull request/)
  assert.throws(() => validatePullRequestNumber('1e2'), /invalid pull request/)
  assert.throws(() => validateBody('## Report'), /missing the expected/)
  assert.throws(() => validateBody(`${marker}\n${'a'.repeat(maximumBodyBytes)}`), /exceeds/)
})

test('creates a comment when no owned marker exists', async () => {
  const calls = []
  const github = fakeGitHub([{id: 11, user: {login: 'someone-else'}, body: `${marker}\nother user`}], calls)
  const id = await updateComment({github, owner: 'owner', repo: 'repo', pullRequestNumber: '7', body: `${marker}\nreport`})
  assert.equal(id, '99')
  assert.deepEqual(calls.map((call) => call.operation), ['create'])
})

test('updates one owned marker comment and removes owned duplicates', async () => {
  const calls = []
  const github = fakeGitHub([
    {id: 10, user: {login: 'github-actions[bot]'}, body: `${marker}\nold`},
    {id: 11, user: {login: 'someone-else'}, body: `${marker}\nother user`},
    {id: 12, user: {login: 'GITHUB-ACTIONS[BOT]'}, body: `${marker}\nduplicate`}
  ], calls)
  const id = await updateComment({github, owner: 'owner', repo: 'repo', pullRequestNumber: 7, body: `${marker}\nnew`})
  assert.equal(id, '10')
  assert.deepEqual(calls, [
    {operation: 'update', commentID: 10},
    {operation: 'delete', commentID: 12}
  ])
})

test('falls back to the standard Actions bot when token identity is unavailable', async () => {
  const calls = []
  const github = fakeGitHub([
    {id: 10, user: {login: 'github-actions[bot]'}, body: `${marker}\nold`}
  ], calls)
  github.graphql = async () => { throw new Error('resource not accessible by integration') }
  const id = await updateComment({github, owner: 'owner', repo: 'repo', pullRequestNumber: '7', body: `${marker}\nnew`})
  assert.equal(id, '10')
  assert.deepEqual(calls, [{operation: 'update', commentID: 10}])
})

function fakeGitHub(comments, calls) {
  const listComments = async () => ({data: comments})
  return {
    graphql: async () => ({viewer: {login: 'github-actions[bot]'}}),
    paginate: async () => comments,
    rest: {
      issues: {
        listComments,
        createComment: async () => {
          calls.push({operation: 'create'})
          return {data: {id: 99}}
        },
        updateComment: async ({comment_id: commentID}) => {
          calls.push({operation: 'update', commentID})
        },
        deleteComment: async ({comment_id: commentID}) => {
          calls.push({operation: 'delete', commentID})
        }
      }
    }
  }
}

test('accepts v1alpha2 dependency reports', () => {
  validateBody('<!-- cloudforge-verification-report:v1alpha2 -->\n## Dependencies\nRedis PASS')
})

test('accepts v1alpha3 reliability reports', () => {
  validateBody('<!-- cloudforge-verification-report:v1alpha3 -->\n## Completed evidence\nPod recovery FAIL')
})

test('accepts v1alpha4 selected workload reports', () => {
  validateBody('<!-- cloudforge-verification-report:v1alpha4 -->\nBuild app=apps/http context=.')
})

test('accepts v1alpha5 explicit test topology reports', () => {
  validateBody('<!-- cloudforge-verification-report:v1alpha5 -->\nTopology origin: explicit test configuration')
})

test('accepts v1alpha6 backend reports and upgrades an owned older comment', async () => {
  const body = '<!-- cloudforge-verification-report:v1alpha6 -->\nCompleted backend evidence'
  validateBody(body)
  const calls = []
  const github = fakeGitHub([{id: 10, user: {login: 'github-actions[bot]'}, body: '<!-- cloudforge-verification-report:v1alpha5 -->\nPrior report'}], calls)
  const id = await updateComment({github, owner: 'owner', repo: 'repo', pullRequestNumber: 7, body})
  assert.equal(id, '10')
  assert.deepEqual(calls, [{operation: 'update', commentID: 10}])
})

test('accepts bounded v1alpha7 worker reports and rejects unknown or malformed markers', () => {
  const workerMarker = '<!-- cloudforge-verification-report:v1alpha7 -->'
  assert.doesNotThrow(() => validateBody(`${workerMarker}\n## Completed evidence\nWorker recovery FAIL\nHeartbeat claim: process_liveness_only`))
  assert.throws(() => validateBody('<!-- cloudforge-verification-report:v1alpha9 -->\nFuture report'), /missing the expected/)
  assert.throws(() => validateBody(`${workerMarker}missing newline`), /missing the expected/)
  assert.throws(() => validateBody(`${workerMarker}\n${'a'.repeat(maximumBodyBytes)}`), /exceeds/)
})

test('upgrades each owned older report to v1alpha8 without rewriting evidence or another user\'s comment', async () => {
  const workerMarker = '<!-- cloudforge-verification-report:v1alpha8 -->'
  const body = `${workerMarker}\n## Completed evidence\nWorker recovery FAIL\nRestoration PASS\nWorker image replacement PASS`
  for (let version = 1; version <= 7; version++) {
    const calls = []
    const github = fakeGitHub([
      {id: 10, user: {login: 'github-actions[bot]'}, body: `<!-- cloudforge-verification-report:v1alpha${version} -->\nPrior report`},
      {id: 11, user: {login: 'someone-else'}, body: `${workerMarker}\nOther user's report`},
      {id: 12, user: {login: 'GITHUB-ACTIONS[BOT]'}, body: `${workerMarker}\nOwned duplicate`}
    ], calls)
    github.rest.issues.updateComment = async ({comment_id: commentID, body: updatedBody}) => {
      assert.equal(updatedBody, body)
      calls.push({operation: 'update', commentID})
    }
    const id = await updateComment({github, owner: 'owner', repo: 'repo', pullRequestNumber: 7, body})
    assert.equal(id, '10')
    assert.deepEqual(calls, [
      {operation: 'update', commentID: 10},
      {operation: 'delete', commentID: 12}
    ])
  }
})


test('live qualification scope cannot edit or delete normal, foreign or other-run comments', async () => {
  const scope = '35714934490-1'
  const scopeMarker = `<!-- cloudforge-comment-qualification:${scope} -->`
  const calls = []
  const github = fakeGitHub([
    {id: 1, user: {login: 'github-actions[bot]'}, body: `${marker}\nNormal report`},
    {id: 2, user: {login: 'github-actions[bot]'}, body: `${marker}\n<!-- cloudforge-comment-qualification:other-run -->\nOther qualification`},
    {id: 3, user: {login: 'someone-else'}, body: `${marker}\n${scopeMarker}\nForeign report`},
    {id: 10, user: {login: 'github-actions[bot]'}, body: `${marker}\n${scopeMarker}\nOld qualification`},
    {id: 12, user: {login: 'GITHUB-ACTIONS[BOT]'}, body: `${marker}\n${scopeMarker}\nDuplicate qualification`}
  ], calls)
  let updated
  const recordUpdate = github.rest.issues.updateComment
  github.rest.issues.updateComment = async (args) => { updated = args.body; return recordUpdate(args) }
  const id = await updateComment({github, owner: 'owner', repo: 'repo', pullRequestNumber: 55,
    body: `${marker}\n**Status:** ERROR\n`, qualificationScope: scope})
  assert.equal(id, '10')
  assert.ok(updated.includes(scopeMarker))
  assert.ok(updated.includes('Live comment qualification'))
  assert.deepEqual(calls, [{operation: 'update', commentID: 10}, {operation: 'delete', commentID: 12}])
})

test('new qualification scope creates without touching existing normal bot report', async () => {
  const calls = []
  const github = fakeGitHub([{id: 1, user: {login: 'github-actions[bot]'}, body: `${marker}\nNormal report`}], calls)
  assert.equal(await updateComment({github, owner: 'owner', repo: 'repo', pullRequestNumber: 55,
    body: `${marker}\n**Status:** WARN\n`, qualificationScope: 'run-1'}), '99')
  assert.deepEqual(calls, [{operation: 'create'}])
})

test('qualification scope survives compact fallback and invalid scopes cause no API mutation', async () => {
  const calls = []
  const github = fakeGitHub([], calls)
  let posted
  github.rest.issues.createComment = async ({body}) => { posted = body; return {data: {id: 99}} }
  await updateComment({github, owner: 'owner', repo: 'repo', pullRequestNumber: 55,
    body: `${marker}\n**Status:** ERROR\n${'x'.repeat(maximumBodyBytes)}`, qualificationScope: 'run-2'})
  assert.ok(posted.includes('<!-- cloudforge-comment-qualification:run-2 -->'))
  assert.ok(posted.includes('compact comment'))
  assert.ok(Buffer.byteLength(posted, 'utf8') <= maximumBodyBytes)
  for (const qualificationScope of ['../wrong', 'x\n<!-- marker -->', 'a'.repeat(81), 1]) {
    await assert.rejects(updateComment({github, owner: 'owner', repo: 'repo', pullRequestNumber: 55,
      body: `${marker}\nreport`, qualificationScope}), /invalid live qualification scope/)
  }
  assert.deepEqual(calls, [])
})


test('ordinary reporter preserves all qualification scopes while updating ordinary duplicates', async () => {
  const calls = []
  const github = fakeGitHub([
    {id: 10, user: {login: 'github-actions[bot]'}, body: `${marker}\n<!-- cloudforge-comment-qualification:run-1 -->\nHistorical qualification`},
    {id: 1, user: {login: 'github-actions[bot]'}, body: `${marker}\nOrdinary report`},
    {id: 12, user: {login: 'github-actions[bot]'}, body: `${marker}\n<!-- cloudforge-comment-qualification:run-2 -->\nCurrent qualification`},
    {id: 2, user: {login: 'github-actions[bot]'}, body: `${marker}\nOrdinary duplicate`}
  ], calls)
  assert.equal(await updateComment({github, owner: 'owner', repo: 'repo', pullRequestNumber: 55,
    body: `${marker}\n**Status:** WARN\n`}), '1')
  assert.deepEqual(calls, [{operation: 'update', commentID: 1}, {operation: 'delete', commentID: 2}])
})

test('ordinary reporter creates its own comment when only qualification comments exist', async () => {
  const calls = []
  const github = fakeGitHub([{id: 10, user: {login: 'github-actions[bot]'},
    body: `${marker}\n<!-- cloudforge-comment-qualification:run-1 -->\nQualification`}], calls)
  assert.equal(await updateComment({github, owner: 'owner', repo: 'repo', pullRequestNumber: 55,
    body: `${marker}\n**Status:** WARN\n`}), '99')
  assert.deepEqual(calls, [{operation: 'create'}])
})
