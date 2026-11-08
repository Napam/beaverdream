# Naming: vocabulary and renames

Status: built. Reviewed by GLM-5.3-Flash; findings are folded in.
The code is the truth now.

## Why

beaverdream is a world builder, not a forum. A world starts as a plain page
with a message board, and the webmaster rebuilds it as people post. The system
prompt already says a post can turn it into "a game, a shop, a city, a world
the posts live in". The README, the seed page and a few code names still
describe the starting board as the product. This note moves the framing and
makes the vocabulary match.

The mechanic stays central: you change a world by posting, not by editing.
There is no editor, no template picker and no settings panel. That limit is
the identity. Do not promise a website builder.

## Vocabulary

One word per job. These are the only meanings allowed.

| Word | Means | Where it may appear |
|---|---|---|
| **world** | The container: one registry row, one SQLite file, a slug, a lifecycle. The product noun. | Lobby, gate, admin, code, DB |
| **board** | The shared feed at `/worlds/{slug}`, and the seed page. People post for each other, not only for Naps. Never the product. | Visitor copy, routes, seed |
| **site** | The document the webmaster writes: one `site_versions` row. | Admin, code, DB |
| **version** | One revision of the site. | Admin, code, DB |
| **play** | Access to a world: play port, play session, play cookie, handoff. The button says **enter**. | Code, UI |
| **page** | What a browser shows. Prose only, never a type name. | Prose |
| **post** | A visitor's message. One `turns` row with `kind = 'post'`. | Visitor copy, DB |
| **turn** | One entry in the room, in order. A post, a changeset, a reply or a server event is a turn. | Code, logs |
| **changeset** | The webmaster's work in a turn: read the room, change the site, write a version, reply. One `turns` row with `kind = 'changeset'`. | Code, admin, logs, DB |
| **round** | One model call inside a changeset. | Code, logs |
| **room** | The conversation. `turns` is its storage. | Prompt, code |
| **summary** | The compacted room. | Admin, code |
| **shared state** | Model-declared values every visitor sees. | Code, DB |
| **holdings** | What one visitor owns of a visitor counter. | Code, DB |
| **journal** | The model-declared append-only feeds. | Code, DB, prompt |
| **save** | Browser storage through `bd.save`/`bd.load`. | Code, prompt |
| **identity** | An account. **visitor** is that identity as a board sees it. | Code, DB |

The loop is **post → changeset → version**. A turn is the room slot; a
changeset is the webmaster's turn inside it. A post and a reply are turns too.

A changeset can start from one post, a batch of posts, or an admin
instruction. A **reply** is the webmaster's post on the board, but it is not a
`kind = 'post'` row: the kind tells them apart.

Rules:

- Visitors hear **world**. The split above is for the admin and the code.
- **board** is the talk surface and the seed, not the category.
- **site** stays the technical word for the document. Do not push it into
  visitor copy, because a world can be a game.
- **page** is never a type name. `web/pages`, `Page*` and a browser page
  already use it.
- A **post** is addressed to the room, not to Naps. Naps is one reader and may
  stay quiet. Most posts are talk, not requests.
- **state** is the umbrella for two stores: **shared state** (everyone sees)
  and **holdings** (one visitor owns). It is not a third concept.

## README

Replace the opening block with:

```md
# beaverdream

<img src="web/assets/beaver.svg" width="240" alt="a beaver dreaming">

Nobody designed this world. It got that way one post at a time.

beaverdream is a world builder. Each world starts as a plain page with a
message board, and a model, the webmaster, rewrites it after every post:
colors, layout, scripts, even the form it hands you. A world can become a game,
a shop, a city, a tool, or anything else a page can be.

Everyone in a world sees the same page. Someone asks for a dark terminal look,
and the next page is dark for all of them. Someone starts a treehouse, and
everyone gets a treehouse. Only the admin panel can put an old version back.

People join with an invite and sign in with a username and password. The admin
creates worlds and can delete them. Each world is its own SQLite file, like a
Minecraft save.

Model-written scripts run in every player's browser, so keep it on a network
you trust. A public deployment needs separate registrable domains for the app
and the worlds.
```

Swap the leftover `board` lines that mean the product:

- "the app on 6930, boards on 6932" becomes "the app on 6930, worlds on 6932"
- "share a board with friends" becomes "share a world with friends"

The hook stays. "Nobody designed this world" keeps the reveal and drops the
forum category.

## Seed page

Pre-production, the world schema lives in one file, `000001_init.sql`, and is
edited in place. The seed is a `site_versions` row with `turn_id IS NULL`.
Change that row's css and html directly in `000001`; do not add a new
migration. At the first production deploy the file freezes, and every later
change is a new numbered migration.

The seed is the row that `SeedSiteVersion` and reset's `MIN(id)` select.

Existing worlds keep their current page. New worlds, heal and reset get the
new seed. That is intended.

The seed keeps the message-board shape: title, one lede, the form, the thread,
a footer. Change the words, not the interaction.

```html
<h1>beaverdream</h1>
<p class="tagline">a world you build together</p>
<main>
  <p class="lede">everyone here sees the same page. Naps is the webmaster. say what this place should become, or just talk.</p>
  <form class="postbox" method="post" action="posts" data-on:submit="@post('posts', {contentType: 'form'}); el.reset()">
    <p class="postbox-title">say something</p>
    <textarea name="body" required maxlength="2000" rows="3" placeholder="a hello, a request, anything" aria-label="post"></textarea>
    <div class="postbox-actions"><span>posting as {{name}}</span><button type="submit">post</button></div>
  </form>
  <div class="thread">{{posts}}</div>
</main>
<footer class="colophon">one page, everyone in it</footer>
```

Add one rule after `.tagline`:

```css
.lede{color:var(--muted);max-width:34em;margin:0 0 1.25em;line-height:1.5}
```

Why this copy:

- It says world, together and everyone sees the same page.
- It names Naps and the webmaster role, so there is a builder, not an anonymous void.
- The placeholder allows a hello or a request, so the board is social as well
  as a way to ask for a build.
- It does not explain the mechanism, so the first post is still the reveal.

Alternative voice, if the first is too warm:

```html
<p class="tagline">a shared world, built one post at a time</p>
<p class="lede">post what this place should be. everyone sees the same page.</p>
<p class="postbox-title">add to the world</p>
```

The phrase test in `cmd/serve/handlers/handlers_test.go` checks that reset
keeps "a small message board". Replace it with a behavior check: `site.ID == 1`
already proves the seed survived, and `site.TurnID.Valid == false` names the
seed directly. Do not test seed wording.

## Gate and lobby copy

| Now | After |
|---|---|
| "board gate · beaverdream" | "world gate · beaverdream" |
| "this board opens in the app" | "this world opens in the app" |
| "the board keeps the app's account" | "the world keeps the app's account" |
| "a world gets its own board and its own file" | "a world gets its own page and its own file" |
| "board ↗" in the admin header | "open world ↗" |

Keep "play" for access, but the button and the gate say "enter". Keep "step
in" and "manage" too.

## URL paths

One-letter path segments are hard to read. Expand them. The app and play ports
keep the same names, so a world is `/worlds/{slug}` on both.

App port:

```text
POST   /worlds/{slug}/play
GET    /admin/worlds/{slug}
GET    /admin/worlds/{slug}/turns
GET    /admin/worlds/{slug}/turns/{id}
GET    /admin/worlds/{slug}/site
GET    /admin/worlds/{slug}/journal
POST   /admin/worlds/{slug}/journal
POST   /admin/worlds/{slug}/journal/{key}/clear
POST   /admin/worlds/{slug}/holdings/{key}/clear
GET    /admin/worlds/{slug}/live
POST   /admin/worlds/{slug}/changeset
POST   /admin/worlds/{slug}/retry/{id}
POST   /admin/worlds/{slug}/stop/{id}
POST   /admin/worlds/{slug}/undo
POST   /admin/worlds/{slug}/heal
POST   /admin/worlds/{slug}/versions/{id}/restore
POST   /admin/worlds/{slug}/reset
POST   /admin/worlds/{slug}/delete
```

Everything else on the app port stays: `/`, `/login`, `/logout`, `/account`,
`/account/name`, `/account/tokens`, `/invite/{code}`, `/admin`,
`/admin/invites`, `/admin/accounts`, `/admin/model`, `POST /admin/worlds`.

Play port:

```text
GET    /worlds/{slug}
GET    /worlds/{slug}/
GET    /worlds/{slug}/live
POST   /worlds/{slug}/posts
GET    /worlds/{slug}/version
GET    /worlds/{slug}/state
POST   /worlds/{slug}/state/{key}
GET    /worlds/{slug}/journal/{key}
POST   /worlds/{slug}/journal/{key}
POST   /worlds/{slug}/errors
GET    /worlds/{slug}/budget
```

`/handoff`, `/` (the gate) and static files stay.

Changes:

- `/w/` becomes `/worlds/`.
- `/admin/w/` becomes `/admin/worlds/`.
- `/j/{key}` becomes `/journal/{key}`.
- The `/x/` namespace disappears. `state`, `errors` and `budget` are named
  directly.
- `POST /admin/worlds/{slug}/state/{key}/clear` becomes
  `/holdings/{key}/clear`, because it clears what visitors hold, not shared
  state.
- `POST /admin/worlds/{id}/delete` becomes `/admin/worlds/{slug}/delete`, so
  the world namespace uses one key.

What stays:

- `#bd-posts`, `bd-state`, `bdLive`, `bd.` and `bd:<world-id>:` are the
  two-letter BeaverDream namespace, not a path abbreviation. Keep them.
- The post anchor stays `#pN`. It is a fragment, not a route segment, and the
  `>>N` text already explains it.

Call sites:

- `cmd/serve/handlers/app_routes.go`: the route tree, `playNotFound`,
  `BoardURL`, the redirects.
- `cmd/serve/handlers/admin.go`: the world base and board path helpers.
- `web/pages/app.templ`: `WorldPlayPath`.
- `lib/webmaster/mutate.go`: the prompt says "such as /w/NAME/" and "posts,
  x/state/KEY, j/KEY".
- `lib/webmaster/site.go` and the runtime tests: `/w/heroes` becomes
  `/worlds/heroes`.
- The handler tests, `AGENTS.md`, `README.md` and `docs/*.md`.

The seed form posts to `action="posts"`, so it survives the move.

## Renames

Do them in this order. Each step ends with `task check.go` and, where a DB
change is involved, `go tool sqlc generate` and `go test ./...`.

### 1. The names that lie

| Now | After | Where |
|---|---|---|
| `site_state` table | `shared_state` | new world migration, `db/queries/state.sql` |
| `ListSiteState`, `GetSiteState`, `UpsertSiteState`, `DeleteSiteState` | `ListSharedState`, `GetSharedState`, `UpsertSharedState`, `DeleteSharedState` | queries, sqlc, call sites |
| sqlc `SiteState`, `UpsertSiteStateParams` | `SharedState`, `UpsertSharedStateParams` | generated, call sites |
| `Site.State` field, `siteWithState` | `Site.Shared`, `siteWithShared` | `lib/webmaster/site.go`, `mutate.go`, `handlers.go:273`, call sites |
| `AppWorld.Board`, `AdminView.Board` | `BoardURL` | `web/pages/app_view.go`, `web/pages/admin.templ`, handlers |
| `ResetBoard` | `ResetWorld` | `cmd/serve/handlers/reset.go`, routes, tests |
| reset logs "Board reset", "Failed to reset the board", "board reset to the original version" | "World reset", "Failed to reset the world", "world reset to the original version" | `reset.go:93,108` |
| reset confirm "reset the board", "feed row", "mutation history" | "reset the world", "journal row", "changeset history" | `web/pages/admin.templ:16,450` |
| `HealBoard` | `HealSite` | `cmd/serve/handlers/admin.go`, routes, tests |
| heal copy "Failed to heal the board", "the board is already plain", "the board went back to the seed", "Board returned to the seed" | "Failed to heal the site", "the site is already plain", "the site went back to the seed", "Site returned to the seed" | `admin.go:567,572`, `handlers.go:1668,1684` |
| `worldBoard` adapter | `worldPlay` (keep `worldAdmin`) | `cmd/serve/handlers/app_routes.go` |
| `kind` param on `mutate`, `mutateBatch`, `mutateLocked`, `mutateHeld`, `beginRun` | `origin` | `cmd/serve/handlers/handlers.go` |
| `TurnWebmaster`, `turns.kind = 'webmaster'`, `ListWebmasterTurns` | `TurnChangeset`, `turns.kind = 'changeset'`, `ListChangesets` | `lib/webmaster/constants.go`, `room.go`, `db/queries/turns.sql`, `summaries.sql`, handlers, admin, tests |
| admin journal selection `feed` (query param, `Panel.Feed`, `journalPath`) | `key` (`Panel.Key`) | `admin_journal.go:36`, `admin_journal_view.go:71`, `admin.templ:139,188`, `admin_journal.templ`, tests, `AGENTS.md:333` |

The logs say "Changeset round started/finished", "Changeset finished" and
"Changeset ended without done". Changeset is the act; turn is the slot.

The engine is renamed too: `Mutator` -> `ChangesetRunner`, `mutate.go` ->
`changeset.go`, `mutate` -> `runChangeset`, `mutateBatch` ->
`changesetBatch`, `AdminMutate` -> `AdminChangeset`, and the admin route
`/mutate` -> `/changeset`.

`site_state` rename needs a world migration:

```sql
ALTER TABLE site_state RENAME TO shared_state;
```

Update `DELETE FROM site_state` in `reset.go:72`, the heal path that calls
`DeleteSiteState` (`handlers.go:1670`), and the raw insert in
`cmd/serve/handlers/visitor_state_test.go:34`. Run `go tool sqlc generate`
after editing the queries.

`state_holdings`, `state_ops` and `state_schema` stay. "State" is the umbrella
for the two stores, so those names are consistent. Only `site_state` claimed
the wrong thing, that it was the site's state.

Use `origin` for the turn source, not `source`: `versions.go:46` already uses
`source` for the version being carried.

### 2. The model contract

| Now | After | Where |
|---|---|---|
| `data-author` DOM attribute, which holds an identity id | `data-author-id` | `lib/webmaster/site.go`, system prompt in `mutate.go`, `AGENTS.md`, tests |

A page already deployed reads `data-author` the moment the server restarts,
before any model turn can fix it. Emit both attributes from `site.go` with the
same id for one release, teach the prompt the new name, then drop
`data-author`. The display name stays in the post text.

Call sites that move together: `lib/webmaster/site.go:192` (render),
`site.go:123` (comment), `mutate.go:1220` (prompt), `AGENTS.md:148`,
`cmd/serve/handlers/app_router_test.go:928`, `lib/webmaster/tools_test.go:535`.

### 3. Docs and comments

- `AGENTS.md` first line: "a world builder whose webmaster rewrites the page as
  people post", not "a message board whose site is rewritten".
- `AGENTS.md`: remove `inject`. No such handler exists.
- `summarize.go`: "a world builder that starts as a small anonymous message
  board".
- The squash removes `000002`, so its stale cookie-id comment goes with it.
  In `000001`, fix the turn-kind comment (it lists `clear` and omits
  `journal`, `state`, `alert`, `reply`) and make the Down section drop every
  table in the final schema, including `state_holdings`, `state_ops` and the
  renamed `shared_state`.
- Admin "compacted memory" becomes "room summary".
- The room log and admin labels say "changeset" for the webmaster's turn. The
  system prompt still says "turn", because that is how the model experiences
  it.

## Persona name: Naps

The model's name is **Naps**, not "webmaster". The role stays **webmaster**:
the name goes where a person is named, the role goes where the job is
explained.

- System prompt: "You are Naps, the webmaster of beaverdream."
- Reply author: **Naps**.
- Summary prompt: "the memory of Naps, the webmaster of beaverdream."
- Seed lede: name Naps and the webmaster role. The first reply signs Naps.
- Admin: Naps where a person is named, "webmaster turn" for the category.

Storage:

- `TurnChangeset = "changeset"` and `turns.kind = 'changeset'` name the
  category. The turn is still a changeset turn.
- New replies store `author = 'Naps'`.
- Old replies store `'webmaster'`. The board knows a reply by `kind = 'reply'`,
  so it can show the current name without a data migration.
- The `.post-webmaster` class stays. It is a category, not a label.

Why Naps: it is a nickname for Naphat, it carries the nap and dream theme of
beaverdream, and it is short in an author line. It is not "Nappy", which reads
as a diaper in British English and as a dated, offensive hair descriptor in
American English.

## Do not rename

`site_versions`, `webmaster.Site`, `SiteDoc`, `set_site`, `edit`, the admin
"site" tab, `pagePrompt`, `PageRunes`, `visitor`, `holdings`, `journal`,
`turns`, `room`, `play`, `gate`, `handoff`, and `webmaster` as a role or turn
category. The persona name changes to Naps, but the role word stays.

Those are either the model contract or already one word per concept. A rename
buys nothing and costs prompt behavior.

## Order of work

1. Squash the world migrations into `000001_init.sql`, regenerate sqlc, and
   rewrite the migration test. The local `data/` files are already deleted.
2. README and the seed copy in `000001`, with the phrase test replaced by a
   behavior check.
3. Gate copy, the admin link label and the URL path expansion.
4. `shared_state` (in the squashed `000001`), sqlc, `Site.Shared` and call
   sites. In the same pass, `turns.kind` `webmaster` -> `changeset`,
   `TurnWebmaster` -> `TurnChangeset`, and `ListWebmasterTurns` ->
   `ListChangesets`.
5. Go-only sweep: `BoardURL`, `ResetWorld`, `HealSite`, `worldPlay`, `origin`,
   `siteWithShared`.
6. `data-author-id`: emit both attributes, update the prompt, drop the old
   attribute later.
7. Admin `feed` to `key`, then the docs and `AGENTS.md`.
8. `docs/README.md` index entry for this note when it is no longer WIP.

## Open questions

- `HealSite` or `HealWorld`. Heal restores the seed site but also clears shared
  state and journal, so the handler name and the comment must agree on scope.
  This note uses `HealSite` because the visible effect is the page.
- When to drop `data-author` after the dual-emit window. One release is the
  default.

## Review notes

GLM-5.3-Flash reviewed the plan and found no migration-safety violation. The
findings folded in above:

- `data-author` must dual-emit, because the renderer rewrites every published
  page the moment the server restarts.
- `state_holdings` and `state_ops` stay; "state" is the umbrella.
- `feed` reaches `Panel.Feed`, `journalPath`, templates, tests and `AGENTS.md`.
- The seed targets `turn_id IS NULL`, not lowest id.
- Use `origin` for the turn source, because `source` is the carried version.
- `siteWithState` becomes `siteWithShared`.
- Reset and heal log and confirm wording moves with the handler names.

The reviewer agreed with the seed targeting, the migration handling, the
phrase-test replacement, `BoardURL`, `worldPlay`, the gate copy and `HealSite`.
The pre-production migration squash and the URL path expansion came after that
review.
