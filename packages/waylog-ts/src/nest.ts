import { middleware as expressMiddleware, useLogger } from "./express.js";

export { useLogger };

export function middleware(config: Parameters<typeof expressMiddleware>[0]) {
  return expressMiddleware(config);
}

export function interceptor(config: Parameters<typeof expressMiddleware>[0]) {
  const mw = expressMiddleware(config);
  return {
    intercept(context: { switchToHttp(): { getRequest(): unknown; getResponse(): unknown } }, next: { handle(): { subscribe(observer: { error(err: unknown): void; complete(): void }): void } }) {
      const http = context.switchToHttp();
      mw(http.getRequest() as any, http.getResponse() as any, (err?: unknown) => {
        if (err) throw err;
      });
      return next.handle();
    },
  };
}
