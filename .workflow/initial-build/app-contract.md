# Shared implementation contract

Module: `orchard.local/orchard`. Go 1.24 standard library preferred, no external dependencies. Binary: `cmd/orchard`. Static assets: `internal/web/assets/index.html`, `app.js`, `style.css`. Core owns all Go files and tests. App packet owns only static assets and `packaging/`.

CLI minimum: `orchard version`, `orchard schema --json`, `orchard init NAME --dir PATH --bundle-id ID --json`, `orchard doctor --json`, `orchard tools --json`, `orchard plan ACTION --project PATH --json`, `orchard run ACTION --project PATH --execute [--confirm] --json`, `orchard app --workspace PATH --listen 127.0.0.1:PORT`. Global `--json` supported consistently. Optional operation inputs: `--ipa PATH`, `--device ID`. Unknown flags must fail.

API uses a session token initially passed as URL fragment `#token=...`. JavaScript removes fragment and sends `Authorization: Bearer TOKEN`. Every API response is `{ok:true,data:...}` or `{ok:false,error:{code,message}}`. Root GET serves assets. API rejects foreign Host/Origin and lacks permissive CORS.

- `GET /api/state`: data is `{version, workspace, projects:[{path,name,bundleId}], tools:[{id,name,status,version,path,detail,installUrl}], capabilities:[{id,title,status,detail}], actions:[{id,title,description,requiresConfirmation}], history:[]}`. Paths for API project operations are relative to workspace.
- `POST /api/projects` body `{name,bundleId,directory}` creates a new project without overwriting and returns `{path,name,bundleId}`.
- `POST /api/plan` body `{action,project,ipa?,device?}` returns `{id,action,title,steps:[{tool,executable,args:[],directory,description}],blockers:[],warnings:[],requiresConfirmation,executable}`. `executable` is a boolean.
- `POST /api/run` body `{planId,confirm:boolean}` executes a previously planned operation, regenerates/validates it, and returns `{id,action,status,exitCode,output,startedAt,finishedAt}`. Never accept browser-supplied executable or argv.

Actions should include setup, build, devices, install, launch, export, store-status, validate, upload, and submit only where exact upstream commands are verified. Unknown/unsupported actions return useful blockers, never pretend success. Long operations may initially use a bounded synchronous request, with visible busy status and duplicate prevention. Cancellation/background jobs are later work if not implemented.

Visual direction: a calm dark development workspace, warm pale text, muted green accent, a narrow sidebar, main project surface, readable inspector/command log. No cards mosaic, marketing hero, external fonts/scripts, fake devices, or fake build success. Actual empty states and prerequisite status are essential. Support keyboard interaction, reduced motion, clear focus, and narrow screens.
