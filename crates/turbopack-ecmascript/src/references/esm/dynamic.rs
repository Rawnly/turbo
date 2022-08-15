use anyhow::Result;
use swc_ecma_ast::{Callee, ExprOrSpread};
use swc_ecma_quote::quote_expr;
use turbo_tasks::{
    primitives::{BoolVc, StringVc},
    Value, ValueToString,
};
use turbopack_core::{
    chunk::{
        AsyncLoadableReference, AsyncLoadableReferenceVc, ChunkableAssetReference,
        ChunkableAssetReferenceVc, ChunkingContextVc,
    },
    context::AssetContextVc,
    reference::{AssetReference, AssetReferenceVc},
    resolve::{parse::RequestVc, ResolveResultVc},
};

use super::super::pattern_mapping::{PatternMapping, PatternMappingVc, ResolveType::EsmAsync};
use crate::{
    chunk::EcmascriptChunkContextVc,
    code_gen::{CodeGenerateable, CodeGenerateableVc, CodeGeneration, CodeGenerationVc},
    create_visitor,
    references::AstPathVc,
    resolve::esm_resolve,
};

#[turbo_tasks::value]
#[derive(Hash, Debug)]
pub struct EsmAsyncAssetReference {
    pub context: AssetContextVc,
    pub request: RequestVc,
    pub path: AstPathVc,
}

#[turbo_tasks::value_impl]
impl EsmAsyncAssetReferenceVc {
    #[turbo_tasks::function]
    pub fn new(context: AssetContextVc, request: RequestVc, path: AstPathVc) -> Self {
        Self::cell(EsmAsyncAssetReference {
            context,
            request,
            path,
        })
    }
}

#[turbo_tasks::value_impl]
impl AssetReference for EsmAsyncAssetReference {
    #[turbo_tasks::function]
    fn resolve_reference(&self) -> ResolveResultVc {
        esm_resolve(self.request, self.context)
    }

    #[turbo_tasks::function]
    async fn description(&self) -> Result<StringVc> {
        Ok(StringVc::cell(format!(
            "dynamic import {}",
            self.request.to_string().await?,
        )))
    }
}

#[turbo_tasks::value_impl]
impl ChunkableAssetReference for EsmAsyncAssetReference {
    #[turbo_tasks::function]
    fn is_chunkable(&self) -> BoolVc {
        BoolVc::cell(true)
    }
}

#[turbo_tasks::value_impl]
impl AsyncLoadableReference for EsmAsyncAssetReference {
    #[turbo_tasks::function]
    fn is_loaded_async(&self) -> BoolVc {
        BoolVc::cell(true)
    }
}

#[turbo_tasks::value_impl]
impl CodeGenerateable for EsmAsyncAssetReference {
    #[turbo_tasks::function]
    async fn code_generation(
        &self,
        chunk_context: EcmascriptChunkContextVc,
        _context: ChunkingContextVc,
    ) -> Result<CodeGenerationVc> {
        let pm = PatternMappingVc::resolve_request(
            chunk_context,
            esm_resolve(self.request, self.context),
            Value::new(EsmAsync),
        )
        .await?;

        let path = &self.path.await?;

        let visitor = if let PatternMapping::Invalid = &*pm {
            create_visitor!(exact path, visit_mut_call_expr(call_expr: &mut CallExpr) {
                let old_args = std::mem::take(&mut call_expr.args);
                let message = match old_args.first() {
                    Some(ExprOrSpread { spread: None, expr }) => {
                        quote_expr!(
                            "'could not resolve \"' + $arg + '\" into a module'",
                            arg: Expr = *expr.clone(),
                        )
                    }
                    // These are SWC bugs: https://github.com/swc-project/swc/issues/5394
                    Some(ExprOrSpread { spread: Some(_), expr: _ }) => {
                        quote_expr!("'spread operator is illegal in import() expressions.'")
                    }
                    _ => {
                        quote_expr!("'import() expressions require at least 1 argument'")
                    }
                };
                let error = quote_expr!(
                    "new Error($message)",
                    message: Expr = *message
                );
                call_expr.callee = Callee::Expr(quote_expr!("Promise.reject"));
                call_expr.args = vec![
                    ExprOrSpread { spread: None, expr: error, },
                ];
            })
        } else {
            create_visitor!(exact path, visit_mut_call_expr(call_expr: &mut CallExpr) {
                let old_args = std::mem::take(&mut call_expr.args);
                let expr = match old_args.into_iter().next() {
                    Some(ExprOrSpread { expr, spread: None }) => pm.apply(*expr),
                    _ => pm.create(),
                };
                if pm.is_internal_import() {
                    call_expr.callee = Callee::Expr(quote_expr!(
                            "__turbopack_require__($arg)",
                            arg: Expr = expr
                    ));
                    call_expr.args = vec![
                        ExprOrSpread { spread: None, expr: quote_expr!("__turbopack_import__") },
                    ];
                } else {
                    call_expr.args = vec![
                        ExprOrSpread { spread: None, expr: box expr }
                    ]
                }

            })
        };

        Ok(CodeGeneration {
            visitors: vec![visitor],
        }
        .into())
    }
}