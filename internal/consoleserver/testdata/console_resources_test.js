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
vm.runInThisContext(applicationSlice('async function openResourceDetail', 'function sessionKey'), {filename: 'app.js'});

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
    resourceDetailYAML: {textContent: ''},
    resourceDetailDialog: {showModal() {}},
  };
  global.state = {resourceDetailGeneration: 0};

  const requests = [];
  global.api = requestPath => {
    const request = deferred();
    requests.push({path: requestPath, ...request});
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
  requests[1].resolve({yaml: 'name: second'});
  await second;
  assert.equal(elements.resourceDetailYAML.textContent, 'name: second');

  requests[0].resolve({yaml: 'name: first'});
  await first;
  assert.equal(elements.resourceDetailYAML.textContent, 'name: second');
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

testResourceDetailsIgnoreStaleResponses()
  .then(() => testRefreshReportsOptionFailures())
  .then(() => process.stdout.write('Console resources tests passed\n'));
