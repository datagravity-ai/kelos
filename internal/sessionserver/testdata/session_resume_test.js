const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const application = fs.readFileSync(path.join(__dirname, '..', 'web', 'app.js'), 'utf8');

function applicationSlice(start, end) {
  const startIndex = application.indexOf(start);
  const endIndex = application.indexOf(end, startIndex);
  assert.notEqual(startIndex, -1, `${start} not found`);
  assert.notEqual(endIndex, -1, `${end} not found`);
  return application.slice(startIndex, endIndex);
}

vm.runInThisContext(applicationSlice('async function requestSessionResume', 'function createWelcome'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('async function loadSessions', 'async function loadConfig'), {filename: 'app.js'});

async function testUserSuspendedSessionResume() {
  const session = {namespace: 'team-a', name: 'chat', phase: 'Suspended', userSuspended: true};
  global.state = {
    namespaceGeneration: 4,
    sessionListGeneration: 0,
    sessions: [session],
    selected: session,
    resumingSession: false,
  };
  global.sessionKey = value => `${value.namespace}/${value.name}`;
  let request;
  let sessionsRendered = 0;
  const headerStates = [];
  const toasts = [];
  global.api = async (requestPath, options) => {
    request = {path: requestPath, options};
    return {...session, userSuspended: false};
  };
  global.renderSessions = () => { sessionsRendered++; };
  global.renderHeader = () => {
    headerStates.push({
      resumingSession: state.resumingSession,
      userSuspended: state.selected.userSuspended,
    });
  };
  global.showToast = message => { toasts.push(message); };

  await resumeSelectedSession();

  assert.equal(request.path, '/api/sessions/team-a/chat/resume');
  assert.deepEqual(request.options, {method: 'POST'});
  assert.equal(state.sessions[0].userSuspended, false);
  assert.equal(state.selected.userSuspended, false);
  assert.equal(state.resumingSession, false);
  assert.equal(sessionsRendered, 1);
  assert.deepEqual(headerStates[headerStates.length - 1], {
    resumingSession: false,
    userSuspended: false,
  });
  assert.deepEqual(toasts, ['Session resume requested']);
}

async function testResumeIgnoresOlderSessionList() {
  const suspended = {namespace: 'team-a', name: 'chat', phase: 'Suspended', userSuspended: true};
  const resumed = {...suspended, userSuspended: false};
  global.state = {
    namespace: 'team-a',
    namespaceGeneration: 4,
    sessionListGeneration: 0,
    sessions: [suspended],
    selected: suspended,
  };
  let resolveList;
  global.api = async (requestPath) => {
    if (requestPath.startsWith('/api/sessions?')) {
      return new Promise(resolve => { resolveList = resolve; });
    }
    return resumed;
  };
  global.renderSessions = () => {};
  global.renderHeader = () => {};

  const loading = loadSessions();
  await requestSessionResume(suspended);
  resolveList([suspended]);
  await loading;

  assert.equal(state.sessions[0].userSuspended, false);
  assert.equal(state.selected.userSuspended, false);
}

testUserSuspendedSessionResume().then(testResumeIgnoresOlderSessionList).then(() => {
  process.stdout.write('Session resume tests passed\n');
});
