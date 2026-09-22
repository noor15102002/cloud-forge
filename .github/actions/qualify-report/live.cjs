'use strict'

const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const crypto = require('node:crypto')
const reporter = require('../report/comment.cjs')
const request = {timeout: 15000}
const hash = text => crypto.createHash('sha256').update(text).digest('hex')
const save = (directory, name, value) => fs.writeFileSync(path.join(directory, name), JSON.stringify(value, null, 2) + '\n')
const describe = comment => ({id: comment.id, url: comment.html_url, login: comment.user?.login,
  type: comment.user?.type, body_sha256: hash(comment.body || ''), body_bytes: Buffer.byteLength(comment.body || '', 'utf8')})

async function inventory(github, owner, repo, issueNumber) {
  const comments = []
  for (let page = 1; page <= 5; page++) {
    const response = await github.rest.issues.listComments({owner, repo, issue_number: issueNumber, per_page: 100, page, request})
    comments.push(...response.data)
    if (response.data.length < 100) return comments
  }
  throw new Error('live qualification comment inventory exceeds its bounded five-page limit')
}

function inputs(options) {
  const issueNumber = reporter.validatePullRequestNumber(options.pullRequestNumber)
  const scopeMarker = reporter.qualificationMarker(options.qualificationScope)
  if (!scopeMarker) throw new Error('live qualification requires an isolated scope')
  return {issueNumber, scopeMarker}
}

async function prepare(options) {
  const {github, owner, repo, outputDirectory, reportPath, version, commit} = options
  const {issueNumber, scopeMarker} = inputs(options)
  if (fs.existsSync(outputDirectory)) throw new Error('preserve previous live qualification output; choose a new directory')
  fs.mkdirSync(outputDirectory, {recursive: true})
  save(outputDirectory, 'result.json', {qualification: 'INCOMPLETE', proof_type: 'LIVE PLATFORM/INTEGRATION PROOF',
    native_result: 'UNKNOWN', native_cleanup_result: 'UNKNOWN', cleanup: 'NOT APPLICABLE',
    scope: options.qualificationScope, stage: 'preparing', started_at: new Date().toISOString()})
  const raw = fs.readFileSync(reportPath)
  assert.ok(raw.length <= 8 * 1024 * 1024, 'native report exceeds the bounded live qualification input')
  const report = JSON.parse(raw)
  assert.deepEqual(report.producer, {version, commit}, 'live report must come from the canonical candidate')
  assert.equal(report.status, 'error', 'this qualification requires the retained operational cleanup ERROR case')
  assert.ok(report.evidence.some(item => item.status === 'fail'), 'original application FAIL must be present')
  assert.ok(report.evidence.some(item => item.experiment_id === 'environment-cleanup' && item.status === 'error'), 'native cleanup observation ERROR must be present')
  // This is an unchanged report from an installed-candidate operational case.
  // Padding used later changes only a separately labelled comment stress input.
  fs.writeFileSync(path.join(outputDirectory, 'verification.json'), raw)
  save(outputDirectory, 'source.json', {report_sha256: hash(raw), producer: report.producer,
    native_status: report.status, native_cleanup_result: 'ERROR', source_report: 'unchanged canonical candidate report',
    proof_type: 'LIVE PLATFORM/INTEGRATION PROOF', scope: options.qualificationScope})
  const pull = await github.rest.pulls.get({owner, repo, pull_number: issueNumber, request})
  assert.equal(pull.data.number, issueNumber, 'target must be the explicitly selected pull request')
  const comments = await inventory(github, owner, repo, issueNumber)
  assert.ok(!comments.some(comment => (comment.body || '').split('\n').includes(scopeMarker)), 'qualification scope already exists; preserve it and use a new attempt')
  save(outputDirectory, 'before.json', comments.map(describe))
  save(outputDirectory, 'target.json', {owner, repo, pull_request: issueNumber, url: pull.data.html_url,
    scope: options.qualificationScope, prepared_at: new Date().toISOString()})
}

async function finish(options) {
  const {github, owner, repo, outputDirectory, markdownPath, artifactURL} = options
  const {issueNumber, scopeMarker} = inputs(options)
  const before = JSON.parse(fs.readFileSync(path.join(outputDirectory, 'before.json'), 'utf8'))
  const target = JSON.parse(fs.readFileSync(path.join(outputDirectory, 'target.json'), 'utf8'))
  assert.deepEqual({owner, repo, pull_request: issueNumber, scope: options.qualificationScope},
    {owner: target.owner, repo: target.repo, pull_request: target.pull_request, scope: target.scope})
  const createdID = Number(options.commentID)
  assert.ok(Number.isSafeInteger(createdID) && createdID > 0 && !before.some(item => item.id === createdID), 'creation must produce a new comment ID')
  const ledger = []
  function event(operation, data) {
    const record = {at: new Date().toISOString(), operation, ...data}
    ledger.push(record)
    fs.appendFileSync(path.join(outputDirectory, 'operations.jsonl'), JSON.stringify(record) + '\n')
  }
  const ownedIDs = new Set([createdID])
  // All mutations after creation are additionally restricted to IDs created by
  // this qualification. The production reporter still applies its own author
  // and exact scope checks; the wrapper refuses any accidental broader mutation.
  const guarded = Object.create(github)
  guarded.rest = {...github.rest, issues: {...github.rest.issues}}
  for (const operation of ['updateComment', 'deleteComment']) {
    guarded.rest.issues[operation] = async args => {
      assert.ok(ownedIDs.has(args.comment_id), 'live qualification attempted to mutate an unrelated comment')
      event(operation + '-attempt', {comment_id: args.comment_id})
      const response = await github.rest.issues[operation]({...args, request})
      event(operation + '-complete', {comment_id: args.comment_id})
      return response
    }
  }
  guarded.rest.issues.createComment = async () => { throw new Error('an update unexpectedly attempted to create a comment') }
  guarded.paginate = async () => inventory(github, owner, repo, issueNumber)
  async function read(id) {
    return (await github.rest.issues.getComment({owner, repo, comment_id: id,
      headers: {accept: 'application/vnd.github.full+json'}, request})).data
  }
  const markdown = fs.readFileSync(markdownPath, 'utf8')
  fs.writeFileSync(path.join(outputDirectory, 'verification.md'), markdown)
  assert.ok(markdown.includes('| **FAIL** |') && markdown.includes('| **ERROR** |'), 'rendered original FAIL and operational ERROR must both be visible')
  try {
    const created = await read(createdID)
    assert.equal(created.user?.type, 'Bot', 'comment must be authored by the workflow bot')
    assert.ok((created.body || '').split('\n').includes(scopeMarker), 'created comment must have this exact qualification scope')
    assert.ok(created.body.includes('**Status:** ERROR'))
    assert.ok(created.body.includes('| **FAIL** |') && created.body.includes('| **ERROR** |'))
    save(outputDirectory, 'created.json', {...describe(created), body: created.body, body_html: created.body_html})
    event('create-observed', {comment_id: createdID, author: created.user.login})
    const update = markdown.replace(/^(\*\*Status:\*\*.*)$/m, '$1 · Live qualification update 2')
    const updatedID = await reporter.updateComment({...options, github: guarded, body: update})
    assert.equal(updatedID, String(createdID), 'ordinary update must retain the created comment ID')
    const updated = await read(createdID)
    assert.ok(updated.body.includes('Live qualification update 2'))
    assert.equal(updated.user.login, created.user.login)
    save(outputDirectory, 'updated.json', {...describe(updated), body: updated.body, body_html: updated.body_html})
    const duplicateBody = `${markdown.split('\n')[0]}\n${scopeMarker}\nLive comment qualification duplicate placeholder; this is not a native result.\n`
    event('duplicate-create-attempt', {})
    const duplicate = (await github.rest.issues.createComment({owner, repo, issue_number: issueNumber, body: duplicateBody, request})).data
    assert.ok(!before.some(item => item.id === duplicate.id) && duplicate.id !== createdID)
    assert.equal(duplicate.user?.login, created.user.login)
    ownedIDs.add(duplicate.id)
    event('duplicate-create-complete', {comment_id: duplicate.id})
    const large = markdown + '\n\n<!-- qualification-size-padding: ' + 'x'.repeat(reporter.maximumBodyBytes) + ' -->\n'
    fs.writeFileSync(path.join(outputDirectory, 'oversized-comment-input.md'), large)
    assert.ok(Buffer.byteLength(large, 'utf8') > reporter.maximumBodyBytes)
    const compactID = await reporter.updateComment({...options, github: guarded, body: large})
    assert.equal(compactID, String(createdID))
    const compact = await read(createdID)
    assert.ok(compact.body.includes('compact comment'))
    assert.ok(compact.body.includes('**Status:** ERROR'))
    assert.ok(compact.body.includes('| **FAIL** |') && compact.body.includes('| **ERROR** |'))
    assert.ok(compact.body.includes(artifactURL), 'compact result must link the trusted full-artifact workflow')
    assert.ok(Buffer.byteLength(compact.body, 'utf8') <= reporter.maximumBodyBytes)
    assert.ok(compact.body.split('\n').includes(scopeMarker))
    assert.equal(compact.user.login, created.user.login)
    assert.equal(typeof compact.body_html, 'string', 'GitHub-rendered HTML must be retained')
    assert.ok(/<table(?:\s|>)/.test(compact.body_html) && compact.body_html.includes('<strong>FAIL</strong>') && compact.body_html.includes('<strong>ERROR</strong>'), 'GitHub rendered HTML must retain both result categories')
    save(outputDirectory, 'compact.json', {...describe(compact), body: compact.body, body_html: compact.body_html})
    const after = await inventory(github, owner, repo, issueNumber)
    save(outputDirectory, 'after.json', after.map(describe))
    assert.ok(!after.some(comment => comment.id === duplicate.id), 'the same-run duplicate must be removed')
    for (const original of before) {
      const current = after.find(comment => comment.id === original.id)
      assert.ok(current, 'pre-existing comment must remain')
      assert.equal(hash(current.body || ''), original.body_sha256, 'pre-existing comment body must remain unchanged')
      assert.equal(current.user?.login, original.login)
    }
    const scoped = after.filter(comment => comment.user?.login === created.user.login && (comment.body || '').split('\n').includes(scopeMarker))
    assert.deepEqual(scoped.map(comment => comment.id), [createdID])
    const result = {qualification: 'PASS', proof_type: 'LIVE PLATFORM/INTEGRATION PROOF',
      native_result: 'ERROR', native_cleanup_result: 'ERROR', native_evidence_preserved: ['FAIL', 'ERROR'], cleanup: 'NOT APPLICABLE',
      creation_id: createdID, update_id: Number(updatedID), compact_id: Number(compactID),
      deleted_same_run_duplicate_id: duplicate.id, author: created.user.login,
      comment_url: compact.html_url, comment_bytes: Buffer.byteLength(compact.body, 'utf8'),
      untouched_existing_comments: before.length, artifact_url: artifactURL, completed_at: new Date().toISOString()}
    save(outputDirectory, 'result.json', result)
    return result
  } catch (error) {
    save(outputDirectory, 'result.json', {qualification: 'FAIL', proof_type: 'LIVE PLATFORM/INTEGRATION PROOF',
      native_result: 'ERROR', native_cleanup_result: 'ERROR', cleanup: 'NOT APPLICABLE', creation_id: createdID,
      completed_at: new Date().toISOString(), error_class: error.name, api_status: error.status || null,
      operations_observed: ledger.length})
    throw error
  }
}

module.exports = {prepare, finish, inventory}
