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

vm.runInThisContext(applicationSlice('async function refreshConsole', 'async function openResourceDetail'), {filename: 'app.js'});
vm.runInThisContext(applicationSlice('function setResourceDetailView', 'function sessionKey'), {filename: 'app.js'});

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return {promise, resolve, reject};
}

async function testResourceDetailsIgnoreStaleResponses() {
  global.elements = {
    resourceDetailTitle: {textContent: ''},
    resourceDetailSubtitle: {textContent: ''},
    resourceDetailTabs: {hidden: false},
    resourceDetailLogsTab: {setAttribute() {}, tabIndex: 0},
    resourceDetailManifestTab: {setAttribute() {}, tabIndex: -1},
    resourceDetailLogsPanel: {hidden: true},
    resourceDetailManifestPanel: {hidden: false},
    refreshResourceLogs: {disabled: true},
    resourceDetailLogs: {textContent: ''},
    resourceDetailYAML: {textContent: ''},
    resourceDetailDialog: {showModal() {}},
  };
  global.state = {
    resourceDetailGeneration: 0,
    resourceDetailLogGeneration: 0,
    resourceDetailTask: null,
  };

  const manifestRequests = [];
  global.api = requestPath => {
    const request = deferred();
    manifestRequests.push({path: requestPath, ...request});
    return request.promise;
  };
  const logRequests = [];
  global.apiText = requestPath => {
    const request = deferred();
    logRequests.push({path: requestPath, ...request});
    return request.promise;
  };

  const first = openResourceDetail(
    {resource: 'tasks', kind: 'Task'},
    {namespace: 'default', name: 'first'},
  );
  const second = openResourceDetail(
    {resource: 'tasks', kind: 'Task'},
    {namespace: 'default', name: 'second'},
  );

  assert.equal(elements.resourceDetailTitle.textContent, 'Task second');
  assert.equal(elements.resourceDetailLogsPanel.hidden, false);
  assert.equal(logRequests[1].path, '/api/resources/tasks/default/second/logs');
  manifestRequests[1].resolve({yaml: 'name: second'});
  logRequests[1].resolve('second logs');
  await second;
  assert.equal(elements.resourceDetailYAML.textContent, 'name: second');
  assert.equal(elements.resourceDetailLogs.textContent, 'second logs');
  assert.equal(elements.refreshResourceLogs.disabled, false);

  manifestRequests[0].resolve({yaml: 'name: first'});
  logRequests[0].resolve('first logs');
  await first;
  assert.equal(elements.resourceDetailYAML.textContent, 'name: second');
  assert.equal(elements.resourceDetailLogs.textContent, 'second logs');
}

async function testRefreshReportsOptionFailures() {
  let message = '';
  global.loadSessions = async () => {};
  global.loadOptions = async () => { throw new Error('options unavailable'); };
  global.loadResources = async () => {};
  global.showToast = value => { message = value; };

  await refreshConsole();

  assert.equal(message, 'options unavailable');
}

function testResourceDetailTabsSupportKeyboardNavigation() {
  let clicked = false;
  let focused = false;
  let prevented = false;
  const logsTab = {};
  const manifestTab = {
    click() { clicked = true; },
    focus() { focused = true; },
  };
  global.elements = {
    resourceDetailLogsTab: logsTab,
    resourceDetailManifestTab: manifestTab,
  };

  handleResourceDetailTabKeydown({
    target: logsTab,
    key: 'ArrowRight',
    preventDefault() { prevented = true; },
  });

  assert.equal(clicked, true);
  assert.equal(focused, true);
  assert.equal(prevented, true);
}

testResourceDetailsIgnoreStaleResponses()
  .then(() => testRefreshReportsOptionFailures())
  .then(() => testResourceDetailTabsSupportKeyboardNavigation())
  .then(() => process.stdout.write('Console resources tests passed\n'));
