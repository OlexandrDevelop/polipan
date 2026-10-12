CREATE TABLE users (
  id bigserial PRIMARY KEY,
  username text NOT NULL,
  username_lower text NOT NULL UNIQUE,
  email_lower text NOT NULL UNIQUE,
  pass_hash text NOT NULL,
  role text NOT NULL DEFAULT 'member' CHECK (role IN ('member', 'moderator', 'admin')),
  banned boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
  token_hash bytea PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL
);
CREATE INDEX sessions_user ON sessions (user_id);
CREATE INDEX sessions_expires ON sessions (expires_at);

CREATE TABLE categories (
  id serial PRIMARY KEY,
  slug text NOT NULL UNIQUE,
  name text NOT NULL,
  description text NOT NULL,
  position int NOT NULL,
  staff_only boolean NOT NULL DEFAULT false
);

CREATE TABLE topics (
  id bigserial PRIMARY KEY,
  category_id int NOT NULL REFERENCES categories (id),
  user_id bigint NOT NULL REFERENCES users (id),
  title text NOT NULL,
  pinned boolean NOT NULL DEFAULT false,
  locked boolean NOT NULL DEFAULT false,
  deleted boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_post_at timestamptz NOT NULL DEFAULT now(),
  reply_count int NOT NULL DEFAULT 0
);
CREATE INDEX topics_list ON topics (category_id, pinned DESC, last_post_at DESC) WHERE NOT deleted;
CREATE INDEX topics_recent ON topics (last_post_at DESC) WHERE NOT deleted;
CREATE INDEX topics_title_fts ON topics USING gin (to_tsvector('simple', title));

CREATE TABLE posts (
  id bigserial PRIMARY KEY,
  topic_id bigint NOT NULL REFERENCES topics (id) ON DELETE CASCADE,
  user_id bigint NOT NULL REFERENCES users (id),
  body text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  edited_at timestamptz,
  deleted boolean NOT NULL DEFAULT false
);
CREATE INDEX posts_topic ON posts (topic_id, id);
CREATE INDEX posts_body_fts ON posts USING gin (to_tsvector('simple', body));

INSERT INTO categories (slug, name, description, position, staff_only) VALUES
  ('announcements', 'Announcements', 'News and releases. Only the team posts here.', 1, true),
  ('help', 'Help', 'Ask a question about installing or using Polipan.', 2, false),
  ('show-your-setup', 'Show your setup', 'Tell us what you run with Polipan.', 3, false),
  ('ideas', 'Ideas', 'Suggest a feature or a change.', 4, false),
  ('off-topic', 'Off topic', 'Anything else about servers and software.', 5, false);
