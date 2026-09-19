'use strict'

const marker = '<!-- cloudforge-verification-report:v1alpha1 -->'
const markers = [marker, '<!-- cloudforge-verification-report:v1alpha2 -->']
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

async function updateComment({ github, owner, repo, pullRequestNumber, body }) {
  const issueNumber = validatePullRequestNumber(pullRequestNumber)
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
  marker,
  maximumBodyBytes,
  updateComment,
  validateBody,
  validatePullRequestNumber
}
