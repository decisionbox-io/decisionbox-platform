// eslint-disable-next-line @typescript-eslint/no-require-imports
const nextJest = require('next/jest');

const createJestConfig = nextJest({ dir: './' });

// react-markdown and its unified/remark/micromark/hast/mdast/unist dependency
// tree ship ESM-only. They must be transformed for Jest, which otherwise
// chokes on `export` in node_modules.
const esmMarkdownDeps = [
  'react-markdown', 'remark.*', 'rehype.*', 'mdast.*', 'micromark.*', 'unist.*',
  'hast.*', 'unified', 'bail', 'trough', 'vfile.*', 'property-information',
  'space-separated-tokens', 'comma-separated-tokens', 'html-url-attributes',
  'decode-named-character-reference', 'character-entities.*', 'devlop', 'ccount',
  'escape-string-regexp', 'markdown-table', 'estree-util-is-identifier-name',
  'style-to-object', 'inline-style-parser', 'longest-streak', 'zwitch',
  'trim-lines', 'is-plain-obj',
].join('|');

/** @type {import('jest').Config} */
const config = {
  testEnvironment: 'jsdom',
  moduleNameMapper: {
    '^@/(.*)$': '<rootDir>/src/$1',
  },
  setupFiles: ['<rootDir>/jest.setup.ts'],
  testPathIgnorePatterns: ['<rootDir>/node_modules/', '<rootDir>/.next/'],
};

// next/jest's default transformIgnorePatterns ignore all of node_modules
// (except Next internals), and Jest combines ignore patterns with OR — so an
// extra "un-ignore" pattern cannot take effect. Inject the ESM markdown deps
// into Next's existing package-allowlist lookahead instead. Done by
// post-processing the generated config so it stays robust across Next versions
// rather than hardcoding Next's pattern.
module.exports = async () => {
  const finalConfig = await createJestConfig(config)();
  finalConfig.transformIgnorePatterns = (finalConfig.transformIgnorePatterns || []).map((p) =>
    p.includes('node_modules') && p.includes('(?!(')
      ? p.replace('(?!(', `(?!(${esmMarkdownDeps}|`)
      : p,
  );
  return finalConfig;
};
