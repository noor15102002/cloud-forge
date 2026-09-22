'use strict'

const assert = require('node:assert/strict')
const test = require('node:test')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const live = require('./live.cjs')
const reporter = require('../report/comment.cjs')
const marker = '<!-- cloudforge-verification-report:v1alpha8 -->'
const markdown = `${marker}\n## CloudForge verification\n\n**Status:** ERROR · **Application:** public-fixture\n\n### Completed evidence\n\n| Experiment | Status | Duration | Result |\n|---|---:|---:|---|\n| Rollout | **FAIL** | 1 ms | requirement unmet |\n| Cleanup | **ERROR** | 1 ms | observation incomplete |\n`

function fixture(t, initial = [], body = markdown) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'cloudforge-live-report-'))
  t.after(() => fs.rmSync(root, {recursive: true, force: true}))
  const reportPath = path.join(root, 'verification.json')
  fs.writeFileSync(reportPath, JSON.stringify({producer: {version: 'v0.1.0-alpha.1', commit: 'a'.repeat(40)},
    status: 'error', evidence: [{experiment_id: 'rolling-deployment', status: 'fail'}, {experiment_id: 'environment-cleanup', status: 'error'}]}))
  const markdownPath = path.join(root, 'verification.md')
  fs.writeFileSync(markdownPath, body)
  const comments = structuredClone(initial)
  const mutations = []
  let nextID = 100
  const rendered = comment => ({...comment, body_html: '<table><tr><td><strong>FAIL</strong></td><td><strong>ERROR</strong></td></tr></table>'})
  const github = {
    graphql: async () => ({viewer: {login: 'github-actions[bot]'}}),
    paginate: async () => comments,
    rest: {
      pulls: {get: async () => ({data: {number: 55, html_url: 'https://github.com/o/r/pull/55'}})},
      issues: {
        listComments: async () => ({data: comments.map(item => ({...item}))}),
        getComment: async ({comment_id}) => ({data: rendered(comments.find(item => item.id === comment_id))}),
        createComment: async ({body}) => {
          const comment = {id: nextID++, user: {login: 'github-actions[bot]', type: 'Bot'}, body,
            html_url: `https://github.com/o/r/pull/55#issuecomment-${nextID - 1}`}
          comments.push(comment)
          mutations.push(['create', comment.id])
          return {data: comment}
        },
        updateComment: async ({comment_id, body}) => {
          comments.find(item => item.id === comment_id).body = body
          mutations.push(['update', comment_id])
          return {data: {id: comment_id}}
        },
        deleteComment: async ({comment_id}) => {
          comments.splice(comments.findIndex(item => item.id === comment_id), 1)
          mutations.push(['delete', comment_id])
          return {status: 204}
        }
      }
    }
  }
  return {github, owner: 'o', repo: 'r', pullRequestNumber: 55, qualificationScope: '12345-1',
    reportPath, markdownPath, outputDirectory: path.join(root, 'output'), version: 'v0.1.0-alpha.1', commit: 'a'.repeat(40),
    artifactURL: 'https://github.com/o/r/actions/runs/12345', comments, mutations}
}

async function create(options) {
  await live.prepare(options)
  return reporter.updateComment({...options, body: fs.readFileSync(options.markdownPath, 'utf8')})
}

test('live protocol proves creation/update/fallback while retaining normal and foreign comments', async t => {
  const initial = [
    {id: 1, user: {login: 'github-actions[bot]', type: 'Bot'}, body: `${marker}\nNormal report`},
    {id: 2, user: {login: 'human', type: 'User'}, body: `${marker}\nHuman comment`},
    {id: 3, user: {login: 'github-actions[bot]', type: 'Bot'}, body: `${marker}\n<!-- cloudforge-comment-qualification:previous-run -->\nPrevious qualification`}
  ]
  const options = fixture(t, initial)
  const commentID = await create(options)
  const result = await live.finish({...options, commentID})
  assert.equal(result.qualification, 'PASS')
  assert.equal(result.creation_id, result.update_id)
  assert.equal(result.update_id, result.compact_id)
  assert.equal(result.untouched_existing_comments, 3)
  assert.deepEqual(options.comments.slice(0, 3), initial)
  assert.deepEqual(options.mutations, [['create', 100], ['update', 100], ['create', 101], ['update', 100], ['delete', 101]])
  const compact = JSON.parse(fs.readFileSync(path.join(options.outputDirectory, 'compact.json')))
  assert.ok(compact.body.includes('| **FAIL** |') && compact.body.includes('| **ERROR** |'))
  assert.ok(compact.body_bytes <= reporter.maximumBodyBytes)
  assert.equal(JSON.parse(fs.readFileSync(path.join(options.outputDirectory, 'verification.json'))).status, 'error')
})

test('already oversized native Markdown still proves a changed update', async t => {
  const options = fixture(t, [], markdown + '\n<!-- optional details ' + 'x'.repeat(65000) + ' -->\n')
  const commentID = await create(options)
  assert.equal((await live.finish({...options, commentID})).qualification, 'PASS')
})

test('scope reuse is rejected before any write', async t => {
  const options = fixture(t, [{id: 1, user: {login: 'github-actions[bot]', type: 'Bot'},
    body: `${marker}\n<!-- cloudforge-comment-qualification:12345-1 -->\nExisting attempt`}])
  await assert.rejects(live.prepare(options), /scope already exists/)
  assert.deepEqual(options.mutations, [])
})

test('wrong producer or missing native failure is rejected before any write', async t => {
  for (const report of [{producer: {version: 'dev', commit: 'a'.repeat(40)}, status: 'error', evidence: [{experiment_id: 'rolling-deployment', status: 'fail'}, {experiment_id: 'environment-cleanup', status: 'error'}]},
    {producer: {version: 'v0.1.0-alpha.1', commit: 'a'.repeat(40)}, status: 'error', evidence: [{experiment_id: 'environment-cleanup', status: 'error'}]}]) {
    const options = fixture(t)
    fs.writeFileSync(options.reportPath, JSON.stringify(report))
    await assert.rejects(live.prepare(options), /canonical candidate|application FAIL/)
    assert.deepEqual(options.mutations, [])
  }
})

test('missing live rendered HTML fails qualification and retains error record', async t => {
  const options = fixture(t)
  const commentID = await create(options)
  options.github.rest.issues.getComment = async ({comment_id}) => ({data: {...options.comments.find(item => item.id === comment_id)}})
  await assert.rejects(live.finish({...options, commentID}), /GitHub-rendered HTML/)
  const result = JSON.parse(fs.readFileSync(path.join(options.outputDirectory, 'result.json')))
  assert.equal(result.qualification, 'FAIL')
  assert.equal(result.native_result, 'ERROR')
})

test('missing scope and oversized inventories cannot silently broaden ownership', async t => {
  const options = fixture(t)
  await assert.rejects(live.prepare({...options, qualificationScope: ''}), /isolated scope/)
  options.github.rest.issues.listComments = async () => ({data: Array.from({length: 100}, (_, id) => ({id, body: ''}))})
  await assert.rejects(live.inventory(options.github, 'o', 'r', 55), /five-page limit/)
  assert.deepEqual(options.mutations, [])
})
