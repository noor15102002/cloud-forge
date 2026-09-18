'use strict'

const marker = '<!-- cloudforge-verification-report:v1alpha1 -->'
const maximumBodyBytes = 60_000

function validatePullRequestNumber(value) {
  const number = Number(value)
  if (!Number.isSafeInteger(number) || number <= 0) {
    throw new Error(`invalid pull request number: ${value}`)
  }
  return number
}

function validateBody(body) {
  if (!body.startsWith(`${marker}\n`)) {
    throw new Error('CloudForge report is missing the expected v1alpha1 marker')
  }
  if (Buffer.byteLength(body, 'utf8') > maximumBodyBytes) {
    throw new Error(`CloudForge report exceeds the ${maximumBodyBytes}-byte comment limit`)
  }
}

async function updateComment({ github, owner, repo, pullRequestNumber, body }) {
  const issueNumber = validatePullRequestNumber(pullRequestNumber)
  validateBody(body)

  const authenticated = await github.rest.users.getAuthenticated()
  const comments = await github.paginate(github.rest.issues.listComments, {
    owner,
    repo,
    issue_number: issueNumber,
    per_page: 100
  })
  const owned = comments.filter((comment) =>
    comment.user &&
    comment.user.id === authenticated.data.id &&
    typeof comment.body === 'string' &&
    comment.body.startsWith(marker)
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
