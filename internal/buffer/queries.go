package buffer

// GraphQL operations, copied from https://developers.buffer.com/reference.md
// on 2026-11-01. See SCHEMA.md for the shapes and open questions.

const qChannels = `query GetChannels($organizationId: OrganizationId!) {
  channels(input: { organizationId: $organizationId }) {
    id
    name
    displayName
    service
    avatar
    isLocked
    isQueuePaused
    isDisconnected
    type
  }
}`

// configuration is marked Experimental in Buffer's reference.
const qConfiguration = `query GetConfiguration($organizationId: OrganizationId!) {
  configuration(input: { organizationId: $organizationId }) {
    services {
      service
      channelType
      content {
        configurationContentTypes
        supportedProperties
        rules {
          __typename
          property
          ... on LengthRule { maxLength }
        }
      }
    }
  }
}`

const qSentPosts = `query GetSentPosts($organizationId: OrganizationId!, $first: Int) {
  posts(
    first: $first
    input: {
      organizationId: $organizationId
      filter: { status: [sent] }
      sort: [{ field: createdAt, direction: desc }]
    }
  ) {
    edges { node { id channelId text status externalLink sentAt createdAt } }
  }
}`

const qPostsCreatedBetween = `query GetPostsCreatedBetween($organizationId: OrganizationId!, $channelId: ChannelId!, $start: DateTime!, $end: DateTime!, $first: Int) {
  posts(
    first: $first
    input: {
      organizationId: $organizationId
      filter: { channelIds: [$channelId], createdAt: { start: $start, end: $end } }
      sort: [{ field: createdAt, direction: asc }]
    }
  ) {
    edges { node { id channelId text status externalLink sentAt createdAt } }
  }
}`

const qCreatePost = `mutation CreatePost($input: CreatePostInput!) {
  createPost(input: $input) {
    __typename
    ... on PostActionSuccess { post { id status } }
    ... on MutationError { message }
  }
}`

const qDailyPostingLimits = `query GetDailyPostingLimits($channelIds: [ChannelId!]!) {
  dailyPostingLimits(input: { channelIds: $channelIds }) {
    channelId
    isAtLimit
    limit
    scheduled
    sent
  }
}`
