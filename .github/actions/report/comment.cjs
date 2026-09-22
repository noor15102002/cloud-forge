'use strict'

const marker = '<!-- cloudforge-verification-report:v1alpha1 -->'
const markers = [marker, '<!-- cloudforge-verification-report:v1alpha2 -->', '<!-- cloudforge-verification-report:v1alpha3 -->', '<!-- cloudforge-verification-report:v1alpha4 -->', '<!-- cloudforge-verification-report:v1alpha5 -->', '<!-- cloudforge-verification-report:v1alpha6 -->', '<!-- cloudforge-verification-report:v1alpha7 -->', '<!-- cloudforge-verification-report:v1alpha8 -->']
const maximumBodyBytes = 60_000

function validatePullRequestNumber(value) {
  const text = String(value)
  if (!/^[1-9][0-9]*$/.test(text)) {
    throw new Error(`invalid pull request number: ${value}`)
  }
  const number = Number(text)
  if (!Number.isSafeInteger(number) || number <= 0) {
    throw new Error(`invalid pull request number: ${value}`)
  }
  return number
}

function validateBody(body) {
  if (!markers.some(value => body.startsWith(`${value}\n`))) {
    throw new Error('CloudForge report is missing the expected versioned marker')
  }
  if (Buffer.byteLength(body, 'utf8') > maximumBodyBytes) {
    throw new Error(`CloudForge report exceeds the ${maximumBodyBytes}-byte comment limit`)
  }
}

// Input is Markdown produced by the trusted, schema-validating renderer. Keep
// complete rows/paragraphs rather than truncating Markdown inside an entity or
// table, and retain the full report in the workflow artifact.
function commentBody(body, artifactURL) {
  if (!markers.some(value => body.startsWith(`${value}\n`))) {
    throw new Error('CloudForge report is missing the expected versioned marker')
  }
  if (Buffer.byteLength(body, 'utf8') <= maximumBodyBytes) {
    return body
  }
  const lines = body.split('\n')
  const statusLine = lines.find(line => line.startsWith('**Status:**') && Buffer.byteLength(line, 'utf8') <= 4000)
  const result = [lines[0], '## CloudForge verification', '', statusLine || 'Status details are retained in the full report.', '', 'This compact comment summarizes a report that exceeds the comment size limit.']
  const evidenceStart = lines.indexOf('### Completed evidence')
  const rows = []
  if (evidenceStart !== -1) {
    for (let index = evidenceStart + 1; index < lines.length; index++) {
      const line = lines[index]
      if (line.startsWith('| ') && /\| \*\*(PASS|WARN|FAIL|BLOCKED|SKIPPED|ERROR)\*\* \|/.test(line)) {
        rows.push(line)
      } else if (rows.length > 0) {
        break
      }
    }
  }
  const statuses = ['ERROR', 'FAIL', 'BLOCKED', 'WARN', 'PASS', 'SKIPPED']
  if (rows.length > 0) {
    result.push('', `Evidence: ${statuses.map(status => `${rows.filter(row => row.includes(`| **${status}** |`)).length} ${status}`).join(', ')}.`)
    const important = statuses.slice(0, 4).flatMap(status => rows.filter(row => row.includes(`| **${status}** |`)))
    if (important.length > 0) {
      const groups = statuses.slice(0, 4).map(status => important.filter(line => line.includes(`| **${status}** |`) && Buffer.byteLength(line, 'utf8') <= 4000))
      // Preserve at least one original FAIL even when later operational errors
      // dominate the report; every omitted row remains in the full artifact.
      const selected = groups.map(group => group[0]).filter(Boolean)
      for (const group of groups) {
        for (const line of group.slice(1)) {
          if (selected.length < 8) selected.push(line)
        }
      }
      result.push('', '### Important evidence', '', '| Experiment | Status | Duration | Result |', '|---|---:|---:|---|', ...selected)
      if (important.length > selected.length) result.push('', `${important.length - selected.length} additional non-passing evidence rows are retained in the full report.`)
    }
  }
  const scanStart = lines.indexOf('### Container scan')
  if (scanStart !== -1) {
    const scan = []
    for (let index = scanStart; index < lines.length && (index === scanStart || !lines[index].startsWith('### ')); index++) {
      scan.push(lines[index])
    }
    if (Buffer.byteLength(scan.join('\n'), 'utf8') <= 8000) result.push('', ...scan)
  }
  const findings = []
  for (let index = 0; index < lines.length && findings.length < 3; index++) {
    if (/^### .+ · (ERROR|FAIL)\/|^### .+ · WARN\/(CRITICAL|HIGH)$/.test(lines[index])) {
      const summary = lines[index + 2] || ''
      if (Buffer.byteLength(lines[index] + summary, 'utf8') <= 3000) findings.push(`${lines[index]}\n\n${summary}`)
    }
  }
  if (findings.length > 0) result.push('', ...findings)
  const validArtifactURL = typeof artifactURL === 'string' && /^https:\/\/[A-Za-z0-9.-]+(?::[0-9]+)?\/[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+\/actions\/runs\/[1-9][0-9]*$/.test(artifactURL)
  result.push('', validArtifactURL
    ? `Full JSON and Markdown reports: [download the workflow artifact](${artifactURL}).`
    : 'Full JSON and Markdown reports are retained in the workflow artifact.')
  const compact = result.join('\n') + '\n'
  validateBody(compact)
  return compact
}

async function updateComment({ github, owner, repo, pullRequestNumber, body, artifactURL }) {
  const issueNumber = validatePullRequestNumber(pullRequestNumber)
  body = commentBody(body, artifactURL)
  validateBody(body)

  let authenticatedLogin = 'github-actions[bot]'
  try {
    const result = await github.graphql('query CloudForgeViewer { viewer { login } }')
    if (result && result.viewer && typeof result.viewer.login === 'string') {
      authenticatedLogin = result.viewer.login
    }
  } catch {
    // Installation tokens cannot call users.getAuthenticated. The standard
    // Actions bot login remains a safe ownership fallback.
  }
  authenticatedLogin = authenticatedLogin.toLowerCase()
  const comments = await github.paginate(github.rest.issues.listComments, {
    owner,
    repo,
    issue_number: issueNumber,
    per_page: 100
  })
  const owned = comments.filter((comment) =>
    comment.user &&
    typeof comment.user.login === 'string' &&
    comment.user.login.toLowerCase() === authenticatedLogin &&
    typeof comment.body === 'string' &&
    markers.some(value => comment.body.startsWith(value))
  )

  let commentID
  if (owned.length === 0) {
    const created = await github.rest.issues.createComment({
      owner,
      repo,
      issue_number: issueNumber,
      body
    })
    commentID = created.data.id
  } else {
    commentID = owned[0].id
    await github.rest.issues.updateComment({
      owner,
      repo,
      comment_id: commentID,
      body
    })
    for (const duplicate of owned.slice(1)) {
      await github.rest.issues.deleteComment({owner, repo, comment_id: duplicate.id})
    }
  }
  return String(commentID)
}

module.exports = {
  commentBody,
  marker,
  maximumBodyBytes,
  updateComment,
  validateBody,
  validatePullRequestNumber
}
