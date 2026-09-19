import http from 'node:http';
import { shared } from '@cloudforge/shared';

const server = http.createServer((request, response) => {
  if (!['/ready', '/health'].includes(request.url)) {
    response.writeHead(404).end();
    return;
  }
  response.setHeader('Content-Type', 'application/json');
  response.setHeader('X-App-Version', process.env.CLOUDFORGE_VERSION || 'a');
  response.end(JSON.stringify({ status: 'ok', shared, version: process.env.CLOUDFORGE_VERSION || 'a' }));
});
server.listen(8080, '0.0.0.0');
process.on('SIGTERM', () => {
  // Continue accepting traffic while EndpointSlice removal propagates.
  setTimeout(() => server.close(() => process.exit(0)), 2000);
});
