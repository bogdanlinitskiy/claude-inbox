'use strict';

const SEC = 1000;
const CLOSE_ARM_MS = 4000;
const TOAST_MS = 6500;
// A run action's output stays up long enough to read.
const RUN_TOAST_MS = 15000;
const RUN_TOAST_LINES = 8;
// The bar renders this many primary actions at most before fitting them into two rows; the menu lists this many
// matches at most. Both keep a config with thousands of actions cheap to draw.
const MAX_CHIPS = 60;
const MAX_MENU_ROWS = 200;
const PRIMARY = 'primary';
const SECONDARY = 'secondary';
// The answer found nothing to answer on the screen (internal/server, codeNothingOnScreen).
const NOTHING_ON_SCREEN = 'NOTHING_ON_SCREEN';
const MAX_TOASTS = 3;
const TITLE = 'Session Inbox';

const KIND = {
  trust: { label: 'Trust folder', cls: 's-wait' },
  question: { label: 'Question', cls: 's-wait' },
  permission: { label: 'Permission', cls: 's-wait' },
  unknown: { label: 'Check terminal', cls: 's-fail' },
  working: { label: 'Working', cls: 's-work' },
  idle: { label: 'Idle', cls: 's-idle' },
  done: { label: 'Done', cls: 's-done' },
  ended: { label: 'Ended', cls: 's-ended' },
};
// A working session of a group that fired nothing for a while (internal/state, EventStalled).
const STALLED = { label: 'No activity', cls: 's-fail' };
const STALLED_TEXT = 'Working, but nothing has happened for several minutes. It may have lost its connection: check the Terminal tab.';
const SINCE_LABEL = { working: 'for', done: 'finished', idle: 'idle for' };
const EMPTY = {
  all: { head: 'No sessions yet', body: 'Start one with New session, or run claude-tmux in a terminal.' },
  needs: { head: 'Nothing is waiting on you', body: 'A session lands here the moment it asks a question or wants a permission.' },
  working: { head: 'Nothing is running', body: 'A session sits here while Claude is working in it.' },
  done: { head: 'Nothing has finished', body: 'A session moves here when it stops, and when it goes idle.' },
  history: { head: 'No past conversations', body: 'A conversation is kept here after its session ends.' },
};
// The diff bar is five blocks wide, like a review tool's.
const DIFF_BLOCKS = 5;
const TAB_LABEL = { terminal: 'Terminal', diff: 'Diff', preview: 'Preview', group: 'Group' };

const sessions = new Map();
// History: past conversations from Claude Code's transcripts that no session in the list holds, latest first.
const past = new Map();
let pastLoaded = false;
let pastQuery = '';
const tabs = new Map();
const drafts = new Map();
let selectedId = null;
const SESSION_HASH = 'session=';
let linkedId = null;
let filter = 'all';
// Set by the ticket chip in a row: the rail narrows to one ticket, still split into state blocks, so the
// readiness of a ticket with several sessions is read off one view instead of counted off the rail.
let ticketFilter = null;
// How many live sessions each ticket has, rebuilt per draw. A row shows its ticket as a chip only when the
// ticket has more than one, because a chip that narrows the rail to a single row buys nothing.
let tixCount = new Map();
// The groups whose rows are folded away under their header, by group id.
const foldedGroups = new Set();
// The messages each group's sessions sent each other, by group id, as last fetched for the Group tab.
const groupMessages = new Map();
let alertsOn = readSetting('alerts', true);
// Sound is off until asked for: a page that beeps unprompted is worse than one that stays quiet. Turning it
// on is the click that lets the browser open an AudioContext at all.
let soundOn = readSetting('sound', false);
let audio = null;
// The text typed in the live filter; it narrows the live list the way the ticket chip narrows it.
let query = '';
let term = null;
let paneKey = '';
let replyKey = '';
const sending = new Set();
// Sessions whose answer found nothing to answer on the screen, by id: the seq of the state it was for. The
// buttons of that state stay locked; a new state brings them back.
const nothingOnScreen = new Map();
let closeArmed = null;
let renamingId = null;
let repoList = null;
// Files the Diff tab shows expanded, by path, so a Refresh keeps them open.
const openDiffFiles = new Set();
// The session the Diff tab last loaded and its uncommitted paths, which the commit dialog commits.
let shownDiff = null;
// GET /api/config: which optional buttons have something behind them (hidden until it says so), the prefix left
// out of repository names, and the origin that may frame the page.
let pageConfig = { ide: false, stripPrefix: '', embedOrigin: '', primaryActions: [], secondaryActions: [] };
// The action waiting for its second click, and the actions on their way, by actionKey.
let armedAction = null;
const runningActions = new Set();
// The open Actions menu: which list, the search, the selected row, and the action whose input field is open.
let menu = null;
let stream = null;
