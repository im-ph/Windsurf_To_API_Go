import { http } from './request';
import type { OAuthLoginBody, WindsurfLoginBody, WindsurfLoginResult } from './types';

export function windsurfLogin(body: WindsurfLoginBody): Promise<WindsurfLoginResult> {
  return http.post('/windsurf-login', body);
}

export function oauthLogin(body: OAuthLoginBody): Promise<WindsurfLoginResult> {
  return http.post('/oauth-login', body);
}
