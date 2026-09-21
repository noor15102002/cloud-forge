'use strict'

const assert = require('node:assert/strict')
const test = require('node:test')

const {
  marker,
  maximumBodyBytes,
  updateComment,
  validateBody,
  validatePullRequestNumber
} = require('./comment.cjs')

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
  assert.throws(() => validateBody('<!-- cloudforge-verification-report:v1alpha8 -->\nFuture report'), /missing the expected/)
  assert.throws(() => validateBody(`${workerMarker}missing newline`), /missing the expected/)
  assert.throws(() => validateBody(`${workerMarker}\n${'a'.repeat(maximumBodyBytes)}`), /exceeds/)
})

test('upgrades each owned older report to v1alpha7 without rewriting evidence or another user\'s comment', async () => {
  const workerMarker = '<!-- cloudforge-verification-report:v1alpha7 -->'
  const body = `${workerMarker}\n## Completed evidence\nWorker recovery FAIL\nRestoration PASS\nWorker image replacement PASS`
  for (let version = 1; version <= 6; version++) {
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
