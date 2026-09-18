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
  assert.throws(() => validateBody('## Report'), /missing the expected/)
  assert.throws(() => validateBody(`${marker}\n${'a'.repeat(maximumBodyBytes)}`), /exceeds/)
})

test('creates a comment when no owned marker exists', async () => {
  const calls = []
  const github = fakeGitHub([{id: 11, user: {id: 2}, body: `${marker}\nother user`}], calls)
  const id = await updateComment({github, owner: 'owner', repo: 'repo', pullRequestNumber: '7', body: `${marker}\nreport`})
  assert.equal(id, '99')
  assert.deepEqual(calls.map((call) => call.operation), ['create'])
})

test('updates one owned marker comment and removes owned duplicates', async () => {
  const calls = []
  const github = fakeGitHub([
    {id: 10, user: {id: 1}, body: `${marker}\nold`},
    {id: 11, user: {id: 2}, body: `${marker}\nother user`},
    {id: 12, user: {id: 1}, body: `${marker}\nduplicate`}
  ], calls)
  const id = await updateComment({github, owner: 'owner', repo: 'repo', pullRequestNumber: 7, body: `${marker}\nnew`})
  assert.equal(id, '10')
  assert.deepEqual(calls, [
    {operation: 'update', commentID: 10},
    {operation: 'delete', commentID: 12}
  ])
})

function fakeGitHub(comments, calls) {
  const listComments = async () => ({data: comments})
  return {
    paginate: async () => comments,
    rest: {
      users: {getAuthenticated: async () => ({data: {id: 1}})},
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
