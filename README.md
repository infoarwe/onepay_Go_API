# BlueTaxi — Backend Go API + Admin Panel

This repo contains the BlueTaxi Go backend (`web/`, `mobile/`) and the React admin panel frontend (`web/src/`), which is served from inside the `web/` module alongside the Go API.

---

## Go API

### Run locally

```bash
cd web
go run .
```

Starts on the port configured in `web/configurations/config.json` (`:8082` by default). This file is gitignored and not tracked in the repo — create it yourself (or get a copy from a teammate) with your own `mgAddrs` (MongoDB connection string), `mgDbName`, `product_key`, and `port` before running. See `web/common/common_config.go`'s `Configuration` struct for the full list of fields it expects.

### Deploy (production server)

This repo (`onepay_Go_API`) is mobile-only - there's no `web/` here (that's a
separate BlueTaxi repo/deployment). It also isn't running as a systemd
service: it's a plain background process, restarted by killing the old PID
and relaunching with `nohup`.

```bash
ssh root@103.235.106.75 -p 4933   # get the current password from a teammate/vault - do not commit it here

cd /root/onepay_Go_API
git pull
cd mobile
go build -o onepay_mobileapi .

# find the currently running instance, then swap it for the new binary
ps aux | grep onepay_mobileapi
kill <pid>
sleep 2
nohup ./onepay_mobileapi > mobileapi.log 2>&1 &
disown

# verify it came back up on :8081
ps aux | grep onepay_mobileapi
ss -ltnp | grep 8081
```

There's a brief gap in service between `kill` and the relaunch - fine for a
low-traffic UAT box, but worth converting to a systemd unit (auto-restart,
no manual PID hunting) before this sees real production load.

---

## React Admin Panel (`web/src`)

A Vite + React 18 single-page app for the BlueTaxi admin dashboard, living inside the Go module's `web/` directory (`web/index.html`, `web/vite.config.js`, `web/package.json`).

### Prerequisites

- Node.js 18+
- The Go API running and reachable (see above)

### Setup

```bash
cd web
npm install
```

Copy `web/.env.example` to `web/.env.local` and fill in real values:

```
VITE_API_BASE_URL=http://localhost:8082
VITE_DOMAIN=uatbluetaxi
VITE_PRODUCT_AUTH=<product_key from configurations/config.json>
VITE_GOOGLE_MAPS_API_KEY=<Google Maps JS API key, for the Booking Heatmap>
```

`.env.local` is gitignored — never commit real values. `.env.example` must stay filled with blank placeholders only.

### Run

```bash
npm run dev         # http://localhost:5173, loads .env.local
npm run dev:uat      # loads .env.uat  (vite --mode uat)
npm run dev:live     # loads .env.live (vite --mode live)
npm run build        # production build -> web/dist
npm run preview      # serve the production build locally
```

`dev:uat` / `dev:live` are for pointing the same codebase at different backend environments without touching source — see `web/.env.uat` / `web/.env.live` (both gitignored; only `.env.example` is tracked). Vite's mode-based env loading picks `.env.<mode>` automatically.

### File structure

```
web/
├── index.html              Vite entry HTML
├── vite.config.js          Vite config (dev server port 5173, build output -> dist/)
├── package.json            npm scripts + dependencies
├── .env.example            Tracked template — blank placeholders only
├── .env.local / .env.uat   Gitignored — real per-environment values
├── .env.live
└── src/
    ├── main.jsx             App entry point (mounts <App /> to #root)
    ├── App.jsx               Top-level providers (AuthProvider) + <AppRoutes />
    │
    ├── routes/
    │   └── AppRoutes.jsx      Route table: /login (public), /dashboard (protected)
    │
    ├── pages/                 One file per routed screen
    │   ├── LoginPage.jsx
    │   └── DashboardPage.jsx   Composes all dashboard/* components, owns the
    │                           global date-range + refresh-tick state
    │
    ├── layouts/
    │   └── DashboardLayout.jsx  Sidebar + Topbar shell wrapping every
    │                             authenticated page
    │
    ├── components/            Shared, reusable UI building blocks
    │   ├── Sidebar.jsx / Topbar.jsx    App chrome (nav, profile, logout)
    │   ├── Card.jsx / Button.jsx / Table.jsx / Loading.jsx / EmptyState.jsx
    │   ├── PasswordField.jsx
    │   ├── ProtectedRoute.jsx / PublicRoute.jsx   Auth-gated route wrappers
    │   ├── DashboardCard.jsx
    │   └── dashboard/          Dashboard-page-specific components only
    │       ├── DashboardHeader.jsx        Title, date-range filter, refresh
    │       ├── DateRangeFilter.jsx        Today/Yesterday/This Month/Last 3
    │       │                              Months/Custom Range picker
    │       ├── KpiGrid.jsx / KpiCard.jsx   KPI cards (revenue, trips, etc.)
    │       ├── PaymentsCard.jsx           Cash/card/wallet donut chart
    │       ├── TripTypeBreakdownCard.jsx  Completed/In Progress/Cancelled
    │       ├── CityTripRequestsCard.jsx   City-based trip request cards
    │       ├── BookingHeatmap.jsx         Google Maps + marker clustering
    │       ├── DriverLoginActivity.jsx    Driver login KPIs + chart
    │       ├── DriverLoginChart.jsx       (presentational bar chart)
    │       ├── TripsOverviewCard.jsx      Rental/Local/Outstation breakdown
    │       ├── DashboardSection.jsx       Generic section/heading wrapper
    │       ├── DashboardSkeleton.jsx      Shared loading-skeleton variants
    │       └── DashboardEmptyState.jsx    Shared empty/unavailable states
    │
    ├── services/               Thin read/write wrappers around the Go API
    │   ├── apiClient.js         Shared axios instance — attaches Domain /
    │   │                        Product-Auth / token headers, handles
    │   │                        session-expiry centrally
    │   ├── authService.js       /admin/login, /admin/logout
    │   ├── dashboardService.js  /dispatch/dashboard/* (all read-only GET)
    │   ├── bookingService.js
    │   ├── driverService.js
    │   ├── customerService.js
    │   └── vehicleService.js
    │
    ├── hooks/
    │   ├── useDashboardSection.js  Fetch/loading/error state for one
    │   │                           dashboard section, with out-of-order
    │   │                           response guarding and a silent-refresh
    │   │                           mode for background polling
    │   └── useVisiblePolling.js    setInterval that only fires while the
    │                                browser tab is visible
    │
    ├── context/
    │   └── AuthContext.jsx      Current admin session (login/logout state)
    │
    ├── utils/
    │   ├── storage.js            Token/admin persistence (localStorage/
    │   │                          sessionStorage)
    │   ├── useAsync.js            Generic async-fetch state hook
    │   ├── dateRange.js           Date-range math (Today/Yesterday/This
    │   │                          Month/Last 3 Months/Current & Previous
    │   │                          Month), all in local time to avoid UTC
    │   │                          day-shift bugs
    │   ├── percentChange.js       Previous-period % change calculation
    │   └── googleMapsLoader.js    Loads the Maps JS API via the modern
    │                               google.maps.importLibrary() bootstrap
    │
    ├── styles/
    │   ├── theme.css              Design tokens (colors, spacing, shadows)
    │   ├── global.css             Base/reset styles
    │   └── forms.css
    │
    └── assets/
        └── logo.svg
```
