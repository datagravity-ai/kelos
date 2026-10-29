#!/usr/bin/env node

const http = require('node:http');

const args = process.argv.slice(2);
if (args[0] !== 'serve') {
  process.stderr.write(`Unsupported command: ${args.join(' ')}\n`);
  process.exit(2);
}

function option(name, fallback) {
  const index = args.indexOf(name);
  return index >= 0 && args[index + 1] ? args[index + 1] : fallback;
}

const hostname = option('--hostname', '127.0.0.1');
const port = Number(option('--port', '4096'));
const sessionID = 'ses-kelos-e2e';
const streams = new Set();

function json(response, status, value) {
  response.writeHead(status, {'content-type': 'application/json'});
  response.end(JSON.stringify(value));
}

function emit(type, properties) {
  const data = JSON.stringify({type, properties});
  for (const stream of streams) stream.write(`data: ${data}\n\n`);
}

function readJSON(request) {
  return new Promise((resolve, reject) => {
    let data = '';
    request.setEncoding('utf8');
    request.on('data', chunk => { data += chunk; });
    request.on('end', () => {
      try {
        resolve(data ? JSON.parse(data) : {});
      } catch (error) {
        reject(error);
      }
    });
    request.on('error', reject);
  });
}

const server = http.createServer(async (request, response) => {
  const url = new URL(request.url, `http://${request.headers.host}`);
  if (request.method === 'GET' && url.pathname === '/global/health') {
    json(response, 200, {healthy: true, version: 'e2e'});
    return;
  }
  if (request.method === 'GET' && url.pathname === '/command') {
    json(response, 200, []);
    return;
  }
  if (request.method === 'GET' && url.pathname === '/event') {
    response.writeHead(200, {
      'content-type': 'text/event-stream',
      'cache-control': 'no-cache',
      connection: 'keep-alive',
    });
    response.write(': connected\n\n');
    streams.add(response);
    request.on('close', () => streams.delete(response));
    return;
  }
  if (request.method === 'POST' && url.pathname === '/session') {
    await readJSON(request);
    json(response, 200, {id: sessionID});
    return;
  }
  if (request.method === 'GET' && url.pathname === `/session/${sessionID}`) {
    json(response, 200, {id: sessionID});
    return;
  }
  if (request.method === 'POST' && url.pathname === `/session/${sessionID}/prompt_async`) {
    const body = await readJSON(request);
    response.writeHead(204);
    response.end();
    const prompt = (body.parts || []).filter(part => part.type === 'text').map(part => part.text).join('');
    setImmediate(() => {
      const messageID = 'msg-assistant';
      const partID = 'part-text';
      emit('session.status', {sessionID, status: {type: 'busy'}});
      emit('message.updated', {info: {id: messageID, sessionID, role: 'assistant'}});
      emit('message.part.updated', {part: {id: partID, sessionID, messageID, type: 'text', text: ''}});
      emit('message.part.delta', {sessionID, messageID, partID, field: 'text', delta: `opencode: ${prompt}`});
      emit('message.part.updated', {part: {id: partID, sessionID, messageID, type: 'text', text: `opencode: ${prompt}`}});
      emit('session.status', {sessionID, status: {type: 'idle'}});
    });
    return;
  }
  if (request.method === 'POST' && url.pathname === `/session/${sessionID}/abort`) {
    json(response, 200, true);
    setImmediate(() => emit('session.status', {sessionID, status: {type: 'idle'}}));
    return;
  }
  json(response, 404, {error: 'not found'});
});

server.listen(port, hostname);

function shutdown() {
  for (const stream of streams) stream.end();
  server.close(() => process.exit(0));
}

process.on('SIGINT', shutdown);
process.on('SIGTERM', shutdown);
